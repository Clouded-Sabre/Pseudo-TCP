package main

import (
	"fmt"
	"log"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/server"
)

var (
	pcpServerObj *server.PcpServer
	err          error
)

func main() {
	// Create PCP server
	pcpServerObj, err = server.NewPcpServer(config.ProtocolID)
	if err != nil {
		log.Println("Error creating PCP server:", err)
		return
	}
	log.Println("PCP server started.")

	// Listen for interrupt signal (Ctrl+C)
	//signalChan := make(chan os.Signal, 1)
	//signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Start the PCP server
	srv, err := pcpServerObj.ListenPcp(config.ServerIP, config.ServerPort)
	if err != nil {
		log.Printf("PCP server error listening at %s:%d: %s", config.ServerIP, config.ServerPort, err)
		return
	}

	log.Printf("PCP service started at %s:%d", config.ServerIP, config.ServerPort)
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

func handleConnection(conn *server.Connection) {
	buffer := make([]byte, 1024)
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
	for i := 0; i < 5; i++ {
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
