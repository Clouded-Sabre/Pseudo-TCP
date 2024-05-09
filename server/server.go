package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/server"
)

var (
	pcpServerObj *server.PcpServer
	err          error
	serverIP     string
	serverPort   int
)

const (
	numOfPackets = 20
	msOfSleep    = 1000
)

func init() {
	// Define CLI flags for server IP and port
	flag.StringVar(&serverIP, "ip", "127.0.0.1", "Server IP address")
	flag.IntVar(&serverPort, "port", 8080, "Server port number")
	flag.Parse()
}

func main() {
	// load config
	config.AppConfig, _ = config.ReadConfig()

	// Create PCP server
	pcpServerObj, err = server.NewPcpServer(uint8(config.AppConfig.ProtocolID))
	if err != nil {
		log.Println("Error creating PCP server:", err)
		return
	}
	log.Println("PCP server started.")

	// Listen for interrupt signal (Ctrl+C)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	closeChan := make(chan struct{})

	// Start the PCP server
	srv, err := pcpServerObj.ListenPcp(serverIP, serverPort)
	if err != nil {
		log.Printf("PCP server error listening at %s:%d: %s", serverIP, serverPort, err)
		return
	}

	log.Printf("PCP service started at %s:%d", serverIP, serverPort)
	// Handle Ctrl+C signal for graceful shutdown
	go func() {
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(closeChan)
		fmt.Println("Closing service.")
		srv.Close() // Close the server gracefully
	}()

	for {
		// Accept incoming connections
		conn, err := srv.Accept()
		if err != nil {
			log.Println("Service stopped accepting new connection.")
			break
		} else {
			go handleConnection(conn, closeChan)
		}
	}
	SleepForMs(2000) // 10 seconds
	log.Println("Server exiting...")
	pcpServerObj.Close()
	SleepForMs(2000) // 10 seconds
	os.Exit(0)
}

func handleConnection(conn *lib.Connection, closeChan chan struct{}) {
	buffer := make([]byte, config.AppConfig.PreferredMSS)
S:
	for {
		select {
		case <-closeChan:
			log.Println("Server app got interuption. Stop and exit.")
			return
		default:
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // read wait for 500 ms
			n, err := conn.Read(buffer)
			if err != nil {
				// Check if the error is a timeout
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Handle timeout error (no data received within the timeout period)
					continue // Continue waiting for incoming packets or handling closeSignal
				}
				if err == io.EOF {
					log.Println("Server app got interuption. Stop and exit.")
					return
				}
				fmt.Println("Error reading packet:", err)
				return
			}
			log.Printf("Got Packet from client(Length %d): %s \n", n, string(buffer[:n]))
			if string(buffer[:n]) == "Client Done" {
				break S
			}
		}
	}

	// Simulate data transmission
	for i := 0; i < numOfPackets; i++ {
		select {
		case <-closeChan:
			log.Println("Server app got interuption. Stop and exit.")
			return
		default:
			// Construct a packet
			payload := []byte(fmt.Sprintf("Data packet %d", i))

			// Send the packet to the server
			conn.Write(payload)
			log.Printf("Packet %d sent.\n", i)

			SleepForMs(msOfSleep) // Simulate some delay between packets
		}

	}

	// Construct a packet
	payload := []byte("Server Done")

	// Send the packet to the server
	conn.Write(payload)
	log.Println("Packet sent:", string(payload))

	conn.Close()
}

// sleep for n milliseconds
func SleepForMs(n int) {
	timeout := time.After(time.Duration(n) * time.Millisecond)
	<-timeout // Wait on the channel
}
