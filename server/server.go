package main

import (
	"fmt"
	"net"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

func main() {
	lib.Connections = make(map[string]*lib.Connection)

	serverConn, err := net.ListenPacket("ip:"+strconv.Itoa(int(lib.ProtocolID)), config.ServerIP)
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer serverConn.Close()

	fmt.Println("Server started")

	// Handle incoming packets
	for {
		buffer := make([]byte, 1024)
		_, addr, err := serverConn.ReadFrom(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			return
		}

		// Process the received packet
		packet := &lib.CustomPacket{}
		packet.Unmarshal(buffer)

		// Extract client IP and port from the packet header
		clientAddr := addr.String()
		clientPort := packet.SourcePort // Extract destination port

		// Check if the packet is intended for the server's port
		if packet.DestinationPort != config.ServerPort {
			fmt.Println("Packet not destined for server port. Ignoring.")
			continue // Ignore the packet
		}

		lib.Mu.Lock()
		defer lib.Mu.Unlock()

		// Check if the connection exists
		connKey := fmt.Sprintf("%s:%d", config.ClientIP, clientPort)
		conn, ok := lib.Connections[connKey]

		if ok {
			// Close connection if FIN received
			if string(packet.Payload) == "FIN" {
				go lib.HandleCloseConnection(serverConn, addr, conn, connKey)
			}

			// Connection exists, acknowledge the received packet and update sequence number
			if packet.SequenceNumber > conn.SequenceNumber {
				go lib.HandleClientRequest(serverConn, addr, conn, connKey)
			}
		} else {
			// Unknown connection, handle 3-way handshake
			if string(packet.Payload) == "SYN" {
				// New connection request
				go lib.HandleNewConnection(serverConn, addr, int(packet.DestinationPort), int(clientPort))
			} else {
				fmt.Println("Unknown connection and packet:", clientAddr, clientPort, string(packet.Payload))
			}
		}
	}
}
