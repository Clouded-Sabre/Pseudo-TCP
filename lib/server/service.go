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
	InputChannel          chan *lib.PcpPacket    // channel for incoming packets of the whole services (including packets for all connections)
	OutputChan            chan *lib.PcpPacket    // output channel for outgoing packets
	connectionMap         map[string]*Connection // open connections
	tempConnMap           map[string]*Connection // temporary connection map for completing 3-way handshake
	newConnChannel        chan *Connection       // new connection all be placed here are 3-way handshake
	ConnCloseSignal       chan *Connection       // signal for connection close
	serviceCloseSignal    chan struct{}          // signal for closing service
}

// NewService creates a new service listening on the specified port.
func newService(pcpProtocolConn *PcpProtocolConnection, serviceAddr net.Addr, port int, outputChan chan *lib.PcpPacket) (*Service, error) {
	return &Service{
		pcpProtocolConnection: pcpProtocolConn,
		ServiceAddr:           serviceAddr,
		Port:                  port,
		InputChannel:          make(chan *lib.PcpPacket),
		OutputChan:            outputChan,
		connectionMap:         make(map[string]*Connection),
		tempConnMap:           make(map[string]*Connection),
		newConnChannel:        make(chan *Connection),
		ConnCloseSignal:       make(chan *Connection),
		serviceCloseSignal:    make(chan struct{}),
	}, nil
}

// Accept accepts incoming connection requests.
func (s *Service) Accept() *Connection {
	for {
		// Wait for new connection to come
		newConn := <-s.newConnChannel

		// Check if the connection exists in the temporary connection map
		_, ok := s.tempConnMap[newConn.attrs.Key]
		if !ok {
			log.Printf("Received ACK packet for non-existent connection: %s. Ignore it!\n", newConn.attrs.Key)
			continue
		}

		// Remove the connection from the temporary connection map
		delete(s.tempConnMap, newConn.attrs.Key)

		// adding the connection to ConnectionMap
		s.connectionMap[newConn.attrs.Key] = newConn

		// start go routine to handle the connection traffic
		go newConn.handleIncomingPackets()

		log.Printf("New connection is ready: %s\n", newConn.attrs.Key)

		return newConn
	}
}

// handleServicePacket is the main service packet dispatches loop.
func (s *Service) handleServicePackets() {
	for {
		select {
		case packet := <-s.InputChannel:
			// Extract SYN and ACK flags from the packet
			isSYN := packet.Flags&lib.SYNFlag != 0
			isACK := packet.Flags&lib.ACKFlag != 0

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
func (s *Service) handleDataPacket(packet *lib.PcpPacket) {
	// Extract destination IP and port from the packet
	sourceIP := packet.SrcAddr.(*net.IPAddr).IP.String()
	sourcePort := packet.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceIP, sourcePort)

	// Check if the connection exists in the connection map
	conn, ok := s.connectionMap[connKey]
	if ok {
		// Dispatch the packet to the corresponding connection's input channel
		conn.attrs.InputChannel <- packet
		return
	}

	// then check if the connection exists in temp connection map
	tempConn, ok := s.tempConnMap[connKey]
	if ok {
		// Dispatch the packet to the corresponding connection's input channel
		tempConn.attrs.InputChannel <- packet
		return
	}

	log.Printf("Received data packet for non-existent connection: %s\n", connKey)
}

// handleSynPacket handles a SYN packet and initiates a new connection.
func (s *Service) handleSynPacket(packet *lib.PcpPacket) {
	// Extract destination IP and port from the packet
	// Extract source IP address and port from the packet
	sourceAddr := packet.SrcAddr
	sourcePort := packet.SourcePort

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

	// TCP options support
	// MSS support negotiation
	if packet.TcpOptions.MSS > 0 {
		if packet.TcpOptions.MSS < newConn.attrs.TcpOptions.MSS {
			newConn.attrs.TcpOptions.MSS = packet.TcpOptions.MSS // default value is config.PreferredMSS
		}
	} else {
		newConn.attrs.TcpOptions.MSS = 0 // disble MSS
	}

	// Window Scaling support
	if packet.TcpOptions.WindowScaleShiftCount == 0 {
		newConn.attrs.TcpOptions.WindowScaleShiftCount = 0 // Disable it
	}

	// SACK support
	if !packet.TcpOptions.SupportSack {
		newConn.attrs.TcpOptions.SupportSack = false // Disable it
	}

	// start the temp connection's goroutine to handle 3-way handshaking process
	go newConn.handle3WayHandshake()

	newConn.OpenServerState = lib.SynReceived // set 3-way handshake state

	// Send SYN-ACK packet to the SYN packet sender
	// Construct a SYN-ACK packet
	newConn.attrs.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1)
	synAckPacket := lib.NewPcpPacket(newConn.attrs.NextSequenceNumber, newConn.attrs.LastAckNumber, lib.SYNFlag|lib.ACKFlag, nil, newConn.attrs)

	// Send the SYN-ACK packet to the sender
	s.OutputChan <- synAckPacket
	newConn.attrs.NextSequenceNumber = uint32(uint64(newConn.attrs.NextSequenceNumber) + 1) // implicit modulo op

	newConn.OpenServerState = lib.SynAckSent // set 3-way handshake state

	log.Printf("Sent SYN-ACK packet to: %s\n", connKey)
}

// handle close connection request from ClientConnection
func (s *Service) handleCloseConnections() {
	for {
		conn := <-s.ConnCloseSignal
		// clear it from p.ConnectionMap
		_, ok := s.connectionMap[conn.attrs.Key] // just make sure it really in ConnectionMap for debug purpose
		if !ok {
			// connection does not exist in ConnectionMap
			log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.attrs.LocalAddr.(*net.IPAddr).IP.String(), conn.attrs.LocalPort, conn.attrs.RemoteAddr.(*net.IPAddr).IP.String(), conn.attrs.RemotePort)
			continue
		}

		// delete the clientConn from ConnectionMap
		delete(s.connectionMap, conn.attrs.Key)
		log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.attrs.LocalAddr.(*net.IPAddr).IP.String(), conn.attrs.LocalPort, conn.attrs.RemoteAddr.(*net.IPAddr).IP.String(), conn.attrs.RemotePort)
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
