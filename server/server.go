package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/shared"
)

func main() {
	conn, err := net.ListenPacket("ip:"+strconv.Itoa(int(shared.ProtocolID)), shared.ServerIP)
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer conn.Close()

	fmt.Println("Server started")

	// Handle incoming packets
	for {
		buffer := make([]byte, 1024)
		_, addr, err := conn.ReadFrom(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			return
		}

		// Process the received packet
		packet := &shared.CustomPacket{}
		packet.Unmarshal(buffer)
		fmt.Printf("Received packet from %s: %s\n", addr.String(), string(packet.Payload))

		// Blindly acknowledge the received packet by sending an ACK back
		ackPacket := shared.NewCustomPacket(packet.AcknowledgmentNum, packet.SequenceNumber+1, []byte("ACK"))
		conn.WriteTo(ackPacket.Marshal(), addr)
	}
}
