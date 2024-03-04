package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/shared"
)

func main() {
	conn, err := net.Dial("ip:"+strconv.Itoa(int(shared.ProtocolID)), shared.ClientIP)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	defer conn.Close()

	fmt.Println("Client connected")

	// Simulate data transmission
	for i := 0; i < 5; i++ {
		// Construct a packet
		packet := shared.NewCustomPacket(uint32(i), uint32(i), []byte(fmt.Sprintf("Data packet %d", i)))

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
	}
}
