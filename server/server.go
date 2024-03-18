package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/server"
)

var (
	pcpServerObj *server.PcpServer
	err          error
)

func main() {
	// Create PCP server
	pcpServerObj, err = server.NewPcpServer(lib.ProtocolID)
	if err != nil {
		fmt.Println("Error creating PCP server:", err)
		return
	}
	fmt.Println("PCP server started.")

	// Listen for interrupt signal (Ctrl+C)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Start the PCP server
	srv, err := pcpServerObj.ListenPcp(config.ServerIP, config.ServerPort)
	if err != nil {
		fmt.Printf("PCP server error listening at %s:%d: %s", config.ServerIP, config.ServerPort, err)
		return
	}

	// Handle Ctrl+C signal for graceful shutdown
	go func() {
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		srv.Close() // Close the server gracefully
		os.Exit(0)
	}()

	// Accept incoming connections
	conn := srv.Accept()
	buffer := make([]byte, 1024)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading packet:", err)
			return
		}
		fmt.Println("Got Packet from client", buffer[:n])
	}
}
