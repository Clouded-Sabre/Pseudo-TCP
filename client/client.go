// client.go

package main

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

func main() {
	// Generate a random client port number
	clientPort := lib.GetAvailableRandomPort()

	// Dial to the server
	conn, err := net.Dial("ip:"+strconv.Itoa(int(lib.ProtocolID)), config.ServerIP)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	defer conn.Close()

	// Initiate a new connection
	err = lib.InitiateNewConnection(conn, clientPort)
	if err != nil {
		fmt.Println("Error initiating connection:", err)
		return
	}

	fmt.Println("Client connected")

	// Simulate data transmission
	for i := 0; i < 5; i++ {
		// Construct a packet
		packet := lib.NewCustomPacket(0, 0, []byte(fmt.Sprintf("Data packet %d", i)))

		// Send the packet to the server
		conn.Write(packet.Marshal())

		// Wait for acknowledgment
		buffer := make([]byte, 1024)
		_, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading acknowledgment:", err)
			return
		}
		fmt.Printf("Received acknowledgment for packet %d\n", i)

		time.Sleep(time.Second) // Simulate some delay between packets
	}

	// Close the connection
	conn.Close()
	fmt.Println("Connection closed")
}
