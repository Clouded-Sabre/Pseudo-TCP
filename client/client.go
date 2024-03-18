// client.go

package main

import (
	"fmt"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/client"
)

func main() {
	pcpCleintObj := client.NewPcpClient(lib.ProtocolID)
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

		time.Sleep(time.Second) // Simulate some delay between packets
	}

	// Close the connection
	conn.Close()
	fmt.Println("PCP Connection closed")
}
