/*
This is a Pseudo-TCP (PCP) server implementation that handles client connections
and simulates network data transmission. The server is designed to work with the
corresponding PCP client for protocol testing and verification.

Key Features:
1. Connection Management:
   - Listens for incoming PCP connections
   - Handles multiple concurrent clients using goroutines
   - Implements graceful shutdown with signal handling (Ctrl+C)
   - Uses WaitGroup for coordinated shutdown

2. Data Exchange Protocol:
   - Receives packets from clients until "Client Done" message
   - Sends 20 sequential response packets ("Data packet X")
   - Sends "Server Done" message before closing connection
   - Implements 500ms read timeout for client messages

3. Error Handling:
   - Connection timeouts
   - EOF detection
   - Network errors
   - Graceful shutdown on interrupt signals
   - Resource cleanup on exit

4. Configuration Options:
   - Server IP address (default: 127.0.0.1)
   - Port number (default: 8080)
   - PCP protocol settings via config.yaml
   - Configurable packet delay (default: 1000ms)

Usage:
  ./server [options]
  Options:
    -ip string    Server IP address (default "127.0.0.1")
    -port int     Server port number (default 8080)

The server operates by:
1. Loading configuration from config.yaml
2. Starting PCP core and listener
3. Accepting client connections
4. For each client:
   - Reading packets until "Client Done"
   - Sending 20 response packets with 1s delays
   - Sending "Server Done" and closing connection
5. Handling graceful shutdown on Ctrl+C

This server implements the PCP protocol for testing network transmission
reliability and performance characteristics.
*/

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	//"github.com/Clouded-Sabre/Pseudo-TCP/lib/server"
)

var (
	pcpCoreObj *lib.PcpCore
	err        error
	serverIP   string
	serverPort int
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
	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	// Create PCP server
	pcpCoreObj, err = lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println("Error creating PCP core:", err)
		return
	}
	log.Println("PCP core started.")

	// Listen for interrupt signal (Ctrl+C)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	closeChan := make(chan struct{})

	// Start the PCP server
	srv, err := pcpCoreObj.ListenPcp(serverIP, serverPort, connConfig)
	if err != nil {
		log.Printf("PCP server error listening at %s:%d: %s", serverIP, serverPort, err)
		return
	}

	log.Printf("PCP service started at %s:%d", serverIP, serverPort)

	var wg sync.WaitGroup
	// Handle Ctrl+C signal for graceful shutdown
	watcher := func(wg *sync.WaitGroup) {
		defer wg.Done()
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(closeChan)
		fmt.Println("Closing service.")
		srv.Close() // Close the server gracefully
	}

	wg.Add(1)
	go watcher(&wg)

	for {
		// Accept incoming connections
		conn, err := srv.Accept()
		if err != nil {
			log.Println("Service stopped accepting new connection.")
			break
		} else {
			wg.Add(1)
			go handleConnection(conn, closeChan, &wg, pcpCoreConfig)
		}
	}

	wg.Wait()
	log.Println("Server exiting...")
	pcpCoreObj.Close()
	os.Exit(0)
}

func handleConnection(conn *lib.Connection, closeChan chan struct{}, wg *sync.WaitGroup, coreConfig *lib.PcpCoreConfig) {
	defer wg.Done()
	buffer := make([]byte, coreConfig.PreferredMSS)
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
