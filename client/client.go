// client.go

package main

import (
	"fmt"
	"log"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/client"
)

func main() {
	pcpCleintObj := client.NewPcpClient(config.ProtocolID)
	// Dial to the server
	conn, err := pcpCleintObj.DialPcp(config.ClientIP, config.ServerIP, config.ServerPort)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	defer conn.Close()

	fmt.Println("PCP connection established!")

	// Simulate data transmission
	for i := 0; i < 5; i++ {
		// Construct a packet
		payload := []byte(fmt.Sprintf("Data packet %d", i))

		// Send the packet to the server
		conn.Write(payload)
		log.Printf("Packet %d sent.\n", i)

		time.Sleep(time.Second) // Simulate some delay between packets
	}

	payload := []byte("Client Done")
	conn.Write(payload)
	log.Println("Packet sent:", string(payload))

	buffer := make([]byte, 1204)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}
		log.Println("Received packet:", string(buffer[:n]))
		if string(buffer[:n]) == "Server Done" {
			break
		}
	}

	// Close the connection
	conn.Close()
	time.Sleep(time.Second * 3)
	fmt.Println("PCP client exit")
}
