package main

import (
	"flag"
	"fmt"
	"log"
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
	//signalChan := make(chan os.Signal, 1)
	//signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Start the PCP server
	srv, err := pcpServerObj.ListenPcp(serverIP, serverPort)
	if err != nil {
		log.Printf("PCP server error listening at %s:%d: %s", serverIP, serverPort, err)
		return
	}

	log.Printf("PCP service started at %s:%d", serverIP, serverPort)
	// Handle Ctrl+C signal for graceful shutdown
	/*go func() {
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		srv.Close() // Close the server gracefully
		os.Exit(0)
	}()*/

	for {
		// Accept incoming connections
		conn := srv.Accept()

		go handleConnection(conn)
	}
}

func handleConnection(conn *lib.Connection) {
	buffer := make([]byte, config.AppConfig.PreferredMSS)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading packet:", err)
			return
		}
		log.Printf("Got Packet from client(Length %d): %s \n", n, string(buffer[:n]))
		if string(buffer[:n]) == "Client Done" {
			break
		}
	}

	// Simulate data transmission
	for i := 0; i < 10; i++ {
		// Construct a packet
		payload := []byte(fmt.Sprintf("Data packet %d", i))

		// Send the packet to the server
		conn.Write(payload)
		log.Printf("Packet %d sent.\n", i)

		time.Sleep(time.Second) // Simulate some delay between packets
	}

	// Construct a packet
	payload := []byte("Server Done")

	// Send the packet to the server
	conn.Write(payload)
	log.Println("Packet sent:", string(payload))

	time.Sleep(time.Second) // Simulate some delay between packets

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading packet:", err)
			return
		}
		log.Printf("Got Packet from client(Length %d): %s \n", n, string(buffer[:n]))
		if string(buffer[:n]) == "Client Done" {
			break
		}
	}
}
