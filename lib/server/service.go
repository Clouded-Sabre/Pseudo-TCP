package server

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// Service represents a service listening on a specific port.
type Service struct {
	pcpProtocolConnection     *PcpProtocolConnection // point back to parent pcp server
	ServiceAddr               net.Addr
	Port                      int
	InputChannel              chan *lib.PcpPacket        // channel for incoming packets of the whole services (including packets for all connections)
	OutputChan, sigOutputChan chan *lib.PcpPacket        // output channels for ordinary outgoing packets and priority signalling packets
	connectionMap             map[string]*lib.Connection // open connections
	tempConnMap               map[string]*lib.Connection // temporary connection map for completing 3-way handshake
	newConnChannel            chan *lib.Connection       // new connection all be placed here are 3-way handshake
	serviceCloseSignal        chan *Service              // signal for sending service close signal to parent pcpProtocolConnection
	ConnCloseSignal           chan *lib.Connection       // signal for connection close
	closeSignal               chan struct{}              // signal for closing service
	connSignalFailed          chan *lib.Connection       // signal for closing temp connection due to openning signalling failed
	wg                        sync.WaitGroup             // WaitGroup to synchronize goroutines
}

// NewService creates a new service listening on the specified port.
func newService(pcpProtocolConn *PcpProtocolConnection, serviceAddr net.Addr, port int, outputChan, sigOutputChan chan *lib.PcpPacket, serviceCloseSignal chan *Service) (*Service, error) {
	newSrv := &Service{
		pcpProtocolConnection: pcpProtocolConn,
		ServiceAddr:           serviceAddr,
		Port:                  port,
		InputChannel:          make(chan *lib.PcpPacket),
		OutputChan:            outputChan,
		sigOutputChan:         sigOutputChan,
		connectionMap:         make(map[string]*lib.Connection),
		tempConnMap:           make(map[string]*lib.Connection),
		newConnChannel:        make(chan *lib.Connection),
		ConnCloseSignal:       make(chan *lib.Connection),
		connSignalFailed:      make(chan *lib.Connection),
		serviceCloseSignal:    serviceCloseSignal,
		closeSignal:           make(chan struct{}),
		wg:                    sync.WaitGroup{},
	}

	// Start goroutines
	newSrv.wg.Add(2) // Increase WaitGroup counter by 3 for the three goroutines
	go newSrv.handleIncomingPackets()
	go newSrv.handleCloseConnections()

	return newSrv, nil
}

// Accept accepts incoming connection requests.
func (s *Service) Accept() *lib.Connection {
	for {
		// Wait for new connection to come
		newConn := <-s.newConnChannel

		// Check if the connection exists in the temporary connection map
		_, ok := s.tempConnMap[newConn.Key]
		if !ok {
			log.Printf("Received ACK packet for non-existent connection: %s. Ignore it!\n", newConn.Key)
			continue
		}

		// Remove the connection from the temporary connection map
		delete(s.tempConnMap, newConn.Key)

		// adding the connection to ConnectionMap
		s.connectionMap[newConn.Key] = newConn

		// start go routine to handle the connection traffic
		newConn.Wg.Add(1)
		go newConn.HandleIncomingPackets()

		log.Printf("New connection is ready: %s\n", newConn.Key)

		return newConn
	}
}

// handleServicePacket is the main service packet dispatches loop.
func (s *Service) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer s.wg.Done()

	for {
		select {
		case <-s.closeSignal:
			// Close the handleServicePackets goroutine to gracefully shutdown
			return
		case packet := <-s.InputChannel:
			if config.Debug && packet.GetChunkReference() != nil {
				packet.GetChunkReference().RemoveFromChannel()
				packet.GetChunkReference().AddCallStack("service.handleServicePackets")
			}
			// Extract SYN and ACK flags from the packet
			isSYN := packet.Flags&lib.SYNFlag != 0
			isACK := packet.Flags&lib.ACKFlag != 0

			// If it's a SYN only packet, handle it
			if isSYN && !isACK {
				s.handleSynPacket(packet)
			} else {
				s.handleDataPacket(packet)
			}
			if config.Debug && packet.GetChunkReference() != nil {
				packet.GetChunkReference().PopCallStack()
			}
		}
	}
}

// handleDataPacket forward Data packet to corresponding open connection if present.
func (s *Service) handleDataPacket(packet *lib.PcpPacket) {
	if config.Debug && packet.GetChunkReference() != nil {
		packet.GetChunkReference().AddCallStack("service.handleDataPacket")
	}
	// Extract destination IP and port from the packet
	sourceIP := packet.SrcAddr.(*net.IPAddr).IP.String()
	sourcePort := packet.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceIP, sourcePort)

	// Check if the connection exists in the connection map
	conn, ok := s.connectionMap[connKey]
	if ok && !conn.IsClosed {
		// Dispatch the packet to the corresponding connection's input channel
		conn.InputChannel <- packet
		if config.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().AddToChannel("Conn.InputChannel")
			packet.GetChunkReference().PopCallStack()
		}
		return
	}

	// then check if the connection exists in temp connection map
	tempConn, ok := s.tempConnMap[connKey]
	if ok && !tempConn.IsClosed {
		if len(packet.Payload) == 0 && packet.SequenceNumber-tempConn.InitialPeerSeq < 2 {
			// Dispatch the packet to the corresponding connection's input channel
			tempConn.InputChannel <- packet
			if config.Debug && packet.GetChunkReference() != nil {
				packet.GetChunkReference().AddToChannel("TempConn.InputChannel")
				packet.GetChunkReference().PopCallStack()
			}
			return
		} else if len(packet.Payload) > 0 {
			// since the connection is not ready yet, discard the data packet for the time being
			packet.ReturnChunk()
			return
		}
	}

	log.Printf("Received data packet for non-existent connection: %s\n", connKey)
	packet.ReturnChunk()
}

// handleSynPacket handles a SYN packet and initiates a new connection.
func (s *Service) handleSynPacket(packet *lib.PcpPacket) {
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

	// then check if the connection exists in temp connection map
	_, ok = s.tempConnMap[connKey]
	if ok {
		log.Printf("Received SYN packet for existing temp connection: %s. Ignore it.\n", connKey)
		return
	}

	// Create a new temporary connection object for the 3-way handshake
	newConn, err := lib.NewConnection(connKey, true, sourceAddr, int(sourcePort), s.ServiceAddr, s.Port, s.OutputChan, s.sigOutputChan, s.ConnCloseSignal, s.newConnChannel, s.connSignalFailed)
	if err != nil {
		log.Printf("Error creating new connection for %s: %s\n", connKey, err)
		return
	}

	newConn.InitServerState = lib.SynReceived

	// Add the new connection to the temporary connection map
	s.tempConnMap[connKey] = newConn

	// TCP options support
	// MSS support negotiation
	if packet.TcpOptions.MSS > 0 {
		if packet.TcpOptions.MSS < newConn.TcpOptions.MSS {
			newConn.TcpOptions.MSS = packet.TcpOptions.MSS // default value is config.PreferredMSS
		}
	} else {
		newConn.TcpOptions.MSS = 0 // disble MSS
	}

	// Window Scaling support
	if packet.TcpOptions.WindowScaleShiftCount == 0 {
		newConn.TcpOptions.WindowScaleShiftCount = 0 // Disable it
	}

	// SACK support
	newConn.TcpOptions.PermitSack = newConn.TcpOptions.PermitSack && packet.TcpOptions.PermitSack    // both sides need to permit SACK
	newConn.TcpOptions.SackEnabled = newConn.TcpOptions.PermitSack && newConn.TcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

	// timestamp support
	newConn.TcpOptions.TimestampEnabled = packet.TcpOptions.TimestampEnabled
	newConn.TcpOptions.TsEchoReplyValue = packet.TcpOptions.Timestamp

	// start the temp connection's goroutine to handle 3-way handshaking process
	newConn.Wg.Add(1)
	go newConn.Handle3WayHandshake()

	// Send SYN-ACK packet to the SYN packet sender
	newConn.LastAckNumber = lib.SeqIncrement(packet.SequenceNumber)
	newConn.InitialPeerSeq = packet.SequenceNumber
	newConn.InitSendSynAck()
	newConn.StartConnSignalTimer()
	newConn.NextSequenceNumber = lib.SeqIncrement(newConn.NextSequenceNumber) // implicit modulo op

	log.Printf("Sent SYN-ACK packet to: %s\n", connKey)
}

// handle close connection request from ClientConnection
func (s *Service) handleCloseConnections() {
	// Decrease WaitGroup counter when the goroutine completes
	defer s.wg.Done()

	for {
		var conn *lib.Connection
		select {
		case conn = <-s.connSignalFailed:
			// clear it from p.ConnectionMap
			_, ok := s.connectionMap[conn.Key] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
				continue
			}
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
			return
		case conn = <-s.ConnCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := s.connectionMap[conn.Key] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(s.connectionMap, conn.Key)
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
		}

	}
}

func (s *Service) Close() error {
	// Close all connections associated with this service
	for _, conn := range s.connectionMap {
		conn.Close()
	}

	// Close all temp connections associated with this service
	for _, tempConn := range s.tempConnMap {
		close(tempConn.ConnSignalFailed)
	}

	// send signal to service go routines to gracefully close them
	close(s.closeSignal)

	s.wg.Wait()

	// close channels created by the service
	close(s.InputChannel)
	close(s.newConnChannel)
	close(s.ConnCloseSignal)
	close(s.connSignalFailed)

	// send signal to parent pcpProtocolConnection to clear service resource
	s.serviceCloseSignal <- s

	// remove the iptable rules for the service
	err := removeIptablesRule(s.ServiceAddr.(*net.IPAddr).IP.String(), s.Port)
	if err != nil {
		log.Printf("Error removing iptable rules for service %s:%d: %s", s.ServiceAddr.(*net.IPAddr).IP.String(), s.Port, err)
		return err
	}

	return nil
}
