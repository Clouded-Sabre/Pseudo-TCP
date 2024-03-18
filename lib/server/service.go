package server

import (
	"fmt"
	"log"
	"net"

	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// Service represents a service listening on a specific port.
type Service struct {
	pcpProtocolConnection *PcpProtocolConnection // point back to parent pcp server
	ServiceAddr           net.Addr
	Port                  int
	InputChannel          chan *lib.PacketVector // channel for incoming packets of the whole services (including packets for all connections)
	//SynChannel     chan *PacketVector     // channel for Syn Packets
	OutputChan         chan *lib.PacketVector // output channel for outgoing packets
	connectionMap      map[string]*Connection // open connections
	tempConnMap        map[string]*Connection // temporary connection map for completing 3-way handshake
	newConnChannel     chan *Connection       // new connection all be placed here are 3-way handshake
	ConnCloseSignal    chan *Connection       // signal for connection close
	serviceCloseSignal chan struct{}          // signal for closing service
}

// NewService creates a new service listening on the specified port.
func newService(pcpProtocolConn *PcpProtocolConnection, serviceAddr net.Addr, port int, outputChan chan *lib.PacketVector) (*Service, error) {
	return &Service{
		pcpProtocolConnection: pcpProtocolConn,
		ServiceAddr:           serviceAddr,
		Port:                  port,
		InputChannel:          make(chan *lib.PacketVector),
		//SynChannel:     make(chan *PacketVector),
		OutputChan:         outputChan,
		connectionMap:      make(map[string]*Connection),
		tempConnMap:        make(map[string]*Connection),
		newConnChannel:     make(chan *Connection),
		serviceCloseSignal: make(chan struct{}),
	}, nil
}

// Accept accepts incoming connection requests.
func (s *Service) Accept() *Connection {
	for {
		// Wait for new connection to come
		newConn := <-s.newConnChannel

		// Check if the connection exists in the temporary connection map
		_, ok := s.tempConnMap[newConn.key]
		if !ok {
			log.Printf("Received ACK packet for non-existent connection: %s. Ignore it!\n", newConn.key)
			continue
		}

		// Remove the connection from the temporary connection map
		delete(s.tempConnMap, newConn.key)

		// adding the connection to ConnectionMap
		s.connectionMap[newConn.key] = newConn

		// start go routine to handle the connection traffic
		go newConn.handleIncomingPackets()

		log.Printf("New connection is ready: %s\n", newConn.key)

		return newConn
	}
}

// handleServicePacket is the main service packet dispatches loop.
func (s *Service) handleServicePackets() {
	for {
		select {
		case packet := <-s.InputChannel:
			// Extract SYN and ACK flags from the packet
			isSYN := packet.Data.Flags&lib.SYNFlag != 0
			isACK := packet.Data.Flags&lib.ACKFlag != 0

			// If it's a SYN only packet, handle it
			if isSYN && !isACK {
				s.handleSynPacket(packet)
			} else {
				s.handleDataPacket(packet)
			}
		case <-s.serviceCloseSignal:
			// Close the handleServicePackets goroutine to gracefully shutdown
			return
		}
	}
}

// handleDataPacket forward Data packet to corresponding open connection if present.
func (s *Service) handleDataPacket(packet *lib.PacketVector) {
	// Extract destination IP and port from the packet
	sourceIP := packet.RemoteAddr.(*net.IPAddr).IP.String()
	sourcePort := packet.Data.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceIP, sourcePort)

	// Check if the connection exists in the connection map
	conn, ok := s.connectionMap[connKey]
	if !ok {
		log.Printf("Received data packet for non-existent connection: %s\n", connKey)
		return
	}

	// Dispatch the packet to the corresponding connection's input channel
	conn.InputChannel <- packet
}

// handleSynPacket handles a SYN packet and initiates a new connection.
func (s *Service) handleSynPacket(packet *lib.PacketVector) {
	// Extract destination IP and port from the packet
	// Extract source IP address and port from the packet
	sourceAddr := packet.RemoteAddr
	sourcePort := packet.Data.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceAddr.(*net.IPAddr).IP.To4().String(), sourcePort)

	// Check if the connection already exists in the connection map
	_, ok := s.connectionMap[connKey]
	if ok {
		log.Printf("Received SYN packet for existing connection: %s. Ignore it.\n", connKey)
		return
	}

	// Create a new temporary connection object for the 3-way handshake
	newConn, err := newConnection(connKey, s, sourceAddr, int(sourcePort), s.ServiceAddr, s.Port)
	if err != nil {
		log.Printf("Error creating new connection for %s: %s\n", connKey, err)
		return
	}

	// Add the new connection to the temporary connection map
	s.tempConnMap[connKey] = newConn
	// start the temp connection's goroutine to handle 3-way handshaking process
	go newConn.handle3WayHandshake()

	newConn.OpenServerState = lib.SynReceived // set 3-way handshake state

	// Send SYN-ACK packet to the SYN packet sender
	// Construct a SYN-ACK packet
	synAckPacket := lib.NewCustomPacket(uint16(s.Port), packet.Data.SourcePort, 0, packet.Data.SequenceNumber+1, lib.SYNFlag|lib.ACKFlag, nil)

	// Send the SYN-ACK packet to the sender
	s.OutputChan <- &lib.PacketVector{Data: synAckPacket, RemoteAddr: packet.RemoteAddr, LocalAddr: packet.LocalAddr}

	newConn.OpenServerState = lib.SynAckSent // set 3-way handshake state

	log.Printf("Sent SYN-ACK packet to: %s\n", connKey)
}

// handle close connection request from ClientConnection
func (s *Service) handleCloseConnections() {
	for {
		conn := <-s.ConnCloseSignal
		// clear it from p.ConnectionMap
		_, ok := s.connectionMap[conn.key] // just make sure it really in ConnectionMap for debug purpose
		if !ok {
			// connection does not exist in ConnectionMap
			log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
			continue
		}

		// delete the clientConn from ConnectionMap
		delete(s.connectionMap, conn.key)
		log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
	}
}

func (s *Service) Close() error {
	// Close all connections associated with this service
	for _, conn := range s.connectionMap {
		conn.Close()
	}

	// send signal to service go routines to gracefully close them
	s.serviceCloseSignal <- struct{}{}
	// send signal to parent pcpProtocolConnection to clear service resource
	s.pcpProtocolConnection.serviceCloseSignal <- s

	return nil
}
