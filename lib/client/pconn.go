package client

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// pcp client protocol connection struct
type pcpProtocolConnection struct {
	ServerAddr, LocalAddr *net.IPAddr
	pConn                 *net.IPConn
	OutputChan            chan *lib.PcpPacket
	ConnectionMap         map[string]*Connection
	tempConnectionMap     map[string]*Connection
	ConnCloseSignalChan   chan *Connection
}

func newPcpProtocolConnection(p *pcpClient, serverAddr, localAddr *net.IPAddr) (*pcpProtocolConnection, error) {
	// Listen on the PCP protocol (20) at the server IP
	pConn, err := net.DialIP("ip:"+strconv.Itoa(int(p.ProtocolID)), localAddr, serverAddr)
	if err != nil {
		fmt.Println("Error listening:", err)
		return nil, err
	}
	//defer pConn.Close()

	pConnection := &pcpProtocolConnection{
		LocalAddr:           localAddr,
		ServerAddr:          serverAddr,
		pConn:               pConn,
		OutputChan:          make(chan *lib.PcpPacket),
		ConnectionMap:       make(map[string]*Connection),
		tempConnectionMap:   make(map[string]*Connection),
		ConnCloseSignalChan: make(chan *Connection),
	}

	go pConnection.handleIncomingPackets()

	go pConnection.handleOutgoingPackets()

	go pConnection.handleCloseConnection()

	return pConnection, nil
}

func (p *pcpProtocolConnection) dial(serverPort int) (*Connection, error) {
	// Choose a random client port
	clientPort := p.getAvailableRandomClientPort()

	// Create connection key
	connKey := fmt.Sprintf("%d:%d", clientPort, serverPort)

	// Create a new temporary connection object for the 3-way handshake
	newConn, err := newConnection(connKey, p, p.ServerAddr, int(serverPort), p.LocalAddr, clientPort)
	if err != nil {
		fmt.Printf("Error creating new connection to %s:%d because of error: %s\n", p.ServerAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.tempConnectionMap[connKey] = newConn

	// Send SYN to server
	synPacket := lib.NewPcpPacket(uint16(clientPort), uint16(serverPort), newConn.nextSequenceNumber, 0, lib.SYNFlag, nil)
	p.OutputChan <- synPacket
	newConn.nextSequenceNumber += 1
	log.Println("Initiated connection to server with connKey:", connKey)

	// Wait for SYN-ACK
	// Create a loop to read from connection's input channel till we see SYN-ACK packet from the other end
	for {
		packet := <-newConn.InputChannel
		if packet.Flags == lib.SYNFlag|lib.ACKFlag { // Verify if it's a SYN-ACK from the server
			// Prepare ACK packet
			newConn.lastAckNumber = packet.SequenceNumber + 1
			ackPacket := lib.NewPcpPacket(uint16(clientPort), uint16(serverPort), newConn.nextSequenceNumber, newConn.lastAckNumber, lib.ACKFlag, nil)
			newConn.OutputChan <- ackPacket
			//newConn.expectedAckNum = 2

			// Connection established, remove newConn from tempClientConnections, and place it into clientConnections pool
			delete(p.tempConnectionMap, connKey)
			p.ConnectionMap[connKey] = newConn

			// start go routine to handle incoming packets for new connection
			go newConn.handleIncomingPackets()

			return newConn, nil
		}
	}
}

// handleServicePacket is the main service packet dispatches loop.
func (p *pcpProtocolConnection) handleIncomingPackets() {
	var (
		err error
		n   int
	)
	buffer := make([]byte, 1024)
	// main loop for incoming packets
	for {
		n, err = p.pConn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}

		// Make a copy of the packet data
		packetData := make([]byte, n)
		copy(packetData, buffer[:n])

		// extract Pcp frame from the received IP frame
		pcpFrame, err := lib.ExtractIpPayload(packetData)
		if err != nil {
			log.Println("Received IP frame is il-formated. Ignore it!")
			continue
		}
		// Extract destination port
		packet := &lib.PcpPacket{}
		packet.Unmarshal(pcpFrame)
		fmt.Println("Received packet Length:", n)
		fmt.Printf("Got packet:\n %+v\n", packet)

		// Extract destination IP and port from the packet
		sourcePort := packet.SourcePort
		destPort := packet.DestinationPort

		// Create connection key. Since it's incoming packet
		// source and destination port needs to be reversed when calculating connection key
		connKey := fmt.Sprintf("%d:%d", destPort, sourcePort)

		// First check if the connection exists in the connection map
		conn, ok := p.ConnectionMap[connKey]
		if ok {
			// open connection. Dispatch the packet to the corresponding connection's input channel
			conn.InputChannel <- packet
			continue
		}

		// then check if packet belongs to an temp connection.
		tempConn, ok := p.tempConnectionMap[connKey]
		if ok {
			// forward to that connection's input channel
			tempConn.InputChannel <- packet
			continue
		}

		fmt.Printf("Received data packet for non-existent connection: %s\n", connKey)
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *pcpProtocolConnection) handleOutgoingPackets() {
	for {
		packet := <-p.OutputChan // Subscribe to p.OutputChan
		if len(packet.Payload) > 0 {
			fmt.Println("outgoing packet payload is", packet.Payload)
		}

		// Marshal the packet into bytes
		frameBytes := packet.Marshal(p.LocalAddr, p.ServerAddr)
		// Write the packet to the interface
		_, err := p.pConn.Write(frameBytes)
		if err != nil {
			fmt.Println("Error writing packet:", err)
			continue
		}
	}
}

// handle close connection request from ClientConnection
func (p *pcpProtocolConnection) handleCloseConnection() {
	for {
		conn := <-p.ConnCloseSignalChan
		// clear it from p.ConnectionMap
		_, ok := p.ConnectionMap[conn.key]
		if !ok {
			// connection does not exist in ConnectionMap
			log.Printf("Pcp Client connection does not exist in %s:%d->%s:%d", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
			continue
		}

		// delete the clientConn from ConnectionMap
		delete(p.ConnectionMap, conn.key)
		log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)

	}
}

func (p *pcpProtocolConnection) getAvailableRandomClientPort() int {
	// Create a map to store all existing client ports
	existingPorts := make(map[int]bool)

	// Populate the map with existing client ports
	for _, conn := range p.ConnectionMap {
		existingPorts[conn.LocalPort] = true
	}

	for _, conn := range p.tempConnectionMap {
		existingPorts[conn.LocalPort] = true
	}

	// Generate a random port number until it's not in the existingPorts map
	var randomPort int
	for {
		randomPort = rand.Intn(config.ClientPortUpper-config.ClientPortLower) + config.ClientPortLower
		if !existingPorts[randomPort] {
			break
		}
	}

	return randomPort
}
