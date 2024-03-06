package lib

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
)

type Connection struct {
	RemoteAddr     string
	RemotePort     int
	LocalAddr      string
	LocalPort      int
	SequenceNumber uint32 // the last acknowledged sequence number
}

var Connections map[string]*Connection
var Mu sync.Mutex

func HandleNewConnection(conn net.PacketConn, remoteAddr net.Addr, localPort, remotePort int) error {
	// Handle 3-way handshake
	// Send SYN-ACK
	synAckPacket := NewCustomPacket(uint16(localPort), uint16(remotePort), 0, 0, []byte("SYN-ACK"))
	conn.WriteTo(synAckPacket.Marshal(), remoteAddr)

	// Wait for ACK
	buffer := make([]byte, 1024)
	_, _, err := conn.ReadFrom(buffer)
	if err != nil {
		fmt.Println("Error reading:", err)
		return err
	}

	// Complete 3-way handshake
	packet := &CustomPacket{}
	packet.Unmarshal(buffer)

	// Verify if it's a ACK from the the other end
	if !bytes.Equal(packet.Payload, []byte("ACK")) || packet.SourcePort != uint16(remotePort) || packet.DestinationPort != uint16(localPort) {
		fmt.Println("Invalid ACK packet received.")
		return errors.New("invalid ACK packet")
	}

	// Connection established, add to connections pool
	connKey := fmt.Sprintf("%s:%d", remoteAddr.(*net.IPAddr).IP.String(), remotePort)
	Connections[connKey] = &Connection{
		LocalAddr:      config.ServerIP,
		LocalPort:      config.ServerPort,
		RemoteAddr:     config.ClientIP,
		RemotePort:     remotePort,
		SequenceNumber: 0,
	}

	// Start handling client requests
	go HandleClientRequest(conn, remoteAddr, Connections[connKey], connKey)

	return nil
}

func HandleClientRequest(conn net.PacketConn, addr net.Addr, connStruct *Connection, connKey string) {
	// Handle client requests
	// Send SYN-ACK
	ackPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), connStruct.SequenceNumber, connStruct.SequenceNumber+1, []byte("ACK"))
	conn.WriteTo(ackPacket.Marshal(), addr)
	fmt.Println("Connection established:", connKey)

	// Simulate data transmission
	for {
		buffer := make([]byte, 1024)
		_, _, err := conn.ReadFrom(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			return
		}

		packet := &CustomPacket{}
		packet.Unmarshal(buffer)

		// Acknowledge the received packet and update sequence number
		if packet.SequenceNumber > connStruct.SequenceNumber {
			ackPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), packet.AcknowledgmentNum, packet.SequenceNumber+1, []byte("ACK"))
			conn.WriteTo(ackPacket.Marshal(), addr)
			connStruct.SequenceNumber = packet.SequenceNumber
			fmt.Printf("Received packet from %s:%d: %s\n", connStruct.RemoteAddr, connStruct.RemotePort, string(packet.Payload))
		}
	}
}

func HandleCloseConnection(conn net.PacketConn, addr net.Addr, connStruct *Connection, connKey string) error {
	// Handle 4-way close handshake
	// Send FIN-ACK
	finAckPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), 0, 0, []byte("FIN-ACK"))
	conn.WriteTo(finAckPacket.Marshal(), addr)

	// Send FIN
	finPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), 0, 0, []byte("FIN"))
	conn.WriteTo(finPacket.Marshal(), addr)

	// Wait for FIN-ACK
	buffer := make([]byte, 1024)
	_, _, err := conn.ReadFrom(buffer)
	if err != nil {
		fmt.Println("Error reading:", err)
		return err
	}

	// Complete 3-way handshake
	packet := &CustomPacket{}
	packet.Unmarshal(buffer)

	// Verify if it's a FIN-ACK from the the other end
	if !bytes.Equal(packet.Payload, []byte("FIN-ACK")) || packet.SourcePort != uint16(connStruct.RemotePort) || packet.DestinationPort != uint16(connStruct.LocalPort) {
		fmt.Println("Invalid FIN-ACK packet received.")
		return errors.New("invalid FIN-ACK packet")
	}

	// Connection closed, remove from connections pool
	delete(Connections, connKey)

	fmt.Println("Connection closed:", connKey)

	return nil
}

func InitiateNewConnection(conn net.PacketConn, clientPort int) (*Connection, error) {
	addr, err := net.ResolveIPAddr("ip", config.ServerIP)
	if err != nil {
		panic(err)
	}
	// Send SYN to server
	synPacket := NewCustomPacket(config.ServerPort, uint16(clientPort), 0, 0, []byte("SYN"))
	conn.WriteTo(synPacket.Marshal(), addr)

	// Wait for SYN-ACK
	buffer := make([]byte, 1024)
	_, _, err = conn.ReadFrom(buffer)
	if err != nil {
		fmt.Println("Error reading:", err)
		return nil, err
	}

	// Complete 3-way handshake
	packet := &CustomPacket{}
	packet.Unmarshal(buffer)

	// Verify if it's a SYN-ACK from the server
	if !bytes.Equal(packet.Payload, []byte("SYN-ACK")) || packet.DestinationPort != uint16(clientPort) || packet.SourcePort != config.ServerPort {
		fmt.Println("Invalid SYN-ACK packet received.")
		return nil, errors.New("invalid SYN-ACK packet")
	}

	// Send ACK
	ackPacket := NewCustomPacket(config.ServerPort, uint16(clientPort), 0, 0, []byte("ACK"))
	conn.WriteTo(ackPacket.Marshal(), addr)

	// Connection established, add to connections pool
	connKey := fmt.Sprintf("%s:%d:%d", config.ServerIP, config.ServerPort, clientPort)
	Connections[connKey] = &Connection{
		RemoteAddr:     config.ServerIP,
		RemotePort:     config.ServerPort,
		LocalAddr:      config.ClientIP,
		LocalPort:      clientPort,
		SequenceNumber: 0,
	}

	return Connections[connKey], nil
}

func SendMessage(conn net.PacketConn, connStruct *Connection, message string) {
	addr, err := net.ResolveIPAddr("ip", connStruct.RemoteAddr)
	if err != nil {
		panic(err)
	}
	// Send data packet
	dataPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), connStruct.SequenceNumber, connStruct.SequenceNumber+1, []byte(message))
	conn.WriteTo(dataPacket.Marshal(), addr)

	// Simulate acknowledgment (in real application, handle acknowledgment separately)
	connStruct.SequenceNumber++
}

func InitiateCloseConnection(conn net.PacketConn, connStruct *Connection) error {
	addr, err := net.ResolveIPAddr("ip", connStruct.RemoteAddr)
	if err != nil {
		panic(err)
	}
	// Send FIN
	finPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), 0, 0, []byte("FIN"))
	conn.WriteTo(finPacket.Marshal(), addr)

	// Wait for FIN-ACK
	buffer := make([]byte, 1024)
	_, _, err = conn.ReadFrom(buffer)
	if err != nil {
		fmt.Println("Error reading:", err)
		return err
	}

	// Complete 3-way handshake
	packet := &CustomPacket{}
	packet.Unmarshal(buffer)

	// Verify if it's a SYN-ACK from the other end
	if !bytes.Equal(packet.Payload, []byte("SYN-ACK")) || packet.DestinationPort != uint16(connStruct.LocalPort) || packet.SourcePort != uint16(connStruct.RemotePort) {
		fmt.Println("Invalid SYN-ACK packet received.")
		return errors.New("invalid SYN-ACK packet")
	}

	// Send ACK for FIN-ACK
	ackPacket := NewCustomPacket(uint16(connStruct.LocalPort), uint16(connStruct.RemotePort), 0, 0, []byte("ACK"))
	conn.WriteTo(ackPacket.Marshal(), addr)

	// Connection closed, remove from connections pool
	connKey := fmt.Sprintf("%s:%d:%d", connStruct.RemoteAddr, connStruct.RemotePort, connStruct.LocalPort)
	delete(Connections, connKey)

	return nil
}

func GetAvailableRandomPort() int {
	// Create a map to store all existing client ports
	existingPorts := make(map[int]bool)

	// Populate the map with existing client ports
	for _, conn := range Connections {
		existingPorts[conn.LocalPort] = true
	}

	// Generate a random port number until it's not in the existingPorts map
	var randomPort int
	for {
		randomPort = rand.Intn(65335-1024) + 1024
		if !existingPorts[randomPort] {
			break
		}
	}

	return randomPort
}
