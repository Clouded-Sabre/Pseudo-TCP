package lib

import (
	"fmt"
	"log"
	"net"
	"sync"

	rp "github.com/Clouded-Sabre/ringpool/lib"
	//"github.com/Clouded-Sabre/Pseudo-TCP/config"
)

// Service represents a service listening on a specific port.
type Service struct {
	// static
	connConfig            *connectionConfig      // Connection Config
	pcpProtocolConnection *PcpProtocolConnection // point back to parent pcp server
	serviceAddr           net.Addr
	port                  int
	// variables
	inputChannel              chan *PcpPacket        // channel for incoming packets of the whole services (including packets for all connections)
	outputChan, sigOutputChan chan *PcpPacket        // output channels for ordinary outgoing packets and priority signalling packets
	connectionMap             map[string]*Connection // open connections
	tempConnMap               map[string]*Connection // temporary connection map for completing 3-way handshake
	newConnChannel            chan *Connection       // new connection all be placed here are 3-way handshake
	serviceCloseSignal        chan *Service          // signal for sending service close signal to parent pcpProtocolConnection
	connCloseSignal           chan *Connection       // signal for connection close
	closeSignal               chan struct{}          // signal for closing service
	connSignalFailed          chan *Connection       // signal for closing temp connection due to openning signalling failed
	wg                        sync.WaitGroup         // WaitGroup to synchronize goroutines
	isClosed                  bool                   // used to denote that the service is close so parent PCP Protocol connection won't forward more packets to it
	mu                        sync.Mutex             // to protect maps
}

// NewService creates a new service listening on the specified port.
func newService(pcpProtocolConn *PcpProtocolConnection, serviceAddr net.Addr, port int, outputChan, sigOutputChan chan *PcpPacket, serviceCloseSignal chan *Service, connConfig *connectionConfig) (*Service, error) {
	newSrv := &Service{
		pcpProtocolConnection: pcpProtocolConn,
		serviceAddr:           serviceAddr,
		port:                  port,
		inputChannel:          make(chan *PcpPacket),
		outputChan:            outputChan,
		sigOutputChan:         sigOutputChan,
		connectionMap:         make(map[string]*Connection),
		tempConnMap:           make(map[string]*Connection),
		newConnChannel:        make(chan *Connection),
		connCloseSignal:       make(chan *Connection),
		connSignalFailed:      make(chan *Connection),
		serviceCloseSignal:    serviceCloseSignal,
		closeSignal:           make(chan struct{}),
		wg:                    sync.WaitGroup{},
		connConfig:            connConfig,
		mu:                    sync.Mutex{},
	}

	// Start goroutines
	newSrv.wg.Add(2) // Increase WaitGroup counter by 3 for the three goroutines
	go newSrv.handleIncomingPackets()
	go newSrv.handleCloseConnections()

	return newSrv, nil
}

// Accept accepts incoming connection requests.
func (s *Service) Accept() (*Connection, error) {
	for {
		select {
		case <-s.closeSignal:
			// Close the accept function to gracefully shutdown
			return nil, net.ErrClosed
		case newConn := <-s.newConnChannel: // Wait for new connection to come

			// Check if the connection exists in the temporary connection map
			s.mu.Lock()
			_, ok := s.tempConnMap[newConn.params.key]
			s.mu.Unlock()
			if !ok {
				log.Printf("Received ACK packet for non-existent connection: %s. Ignore it!\n", newConn.params.key)
				continue
			}

			s.mu.Lock()
			// Remove the connection from the temporary connection map
			delete(s.tempConnMap, newConn.params.key)

			// adding the connection to ConnectionMap
			s.connectionMap[newConn.params.key] = newConn
			s.mu.Unlock()

			// start go routine to handle the connection traffic
			newConn.wg.Add(1)
			go newConn.handleIncomingPackets()

			log.Printf("New connection is ready: %s\n", newConn.params.key)

			return newConn, nil
		}
	}
}

// handleServicePacket is the main service packet dispatches loop.
func (s *Service) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer s.wg.Done()

	var fp int
	for {
		select {
		case <-s.closeSignal:
			log.Println("Closing service handleIncomingPackets go routine")
			// Close the handleServicePackets goroutine to gracefully shutdown
			return
		case packet := <-s.inputChannel:
			if rp.Debug && packet.GetChunkReference() != nil {
				err := packet.TickChannel()
				if err != nil {
					log.Println("Service.handleIncomingPackets:", err)
				}
				fp = packet.AddFootPrint("service.handleServicePackets")
			}
			// Extract SYN and ACK flags from the packet
			isSYN := packet.Flags&SYNFlag != 0
			isACK := packet.Flags&ACKFlag != 0

			// If it's a SYN only packet, handle it
			if isSYN && !isACK {
				// no need to tick zero length SYN packet
				s.handleSynPacket(packet)
			} else {
				if rp.Debug && packet.GetChunkReference() != nil {
					packet.TickFootPrint(fp)
				}
				s.handleOtherPacket(packet)
			}
		}
	}
}

// handleDataPacket forward non-syn packet to corresponding open connection if present.
func (s *Service) handleOtherPacket(packet *PcpPacket) {
	var fp int
	if rp.Debug && packet.GetChunkReference() != nil {
		fp = packet.AddFootPrint("service.handleDataPacket")
	}
	// Extract destination IP and port from the packet
	sourceIP := packet.SrcAddr.(*net.IPAddr).IP.String()
	sourcePort := packet.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceIP, sourcePort)

	// Check if the connection exists in the connection map
	s.mu.Lock()
	conn, ok := s.connectionMap[connKey]
	s.mu.Unlock()
	if ok {
		conn.isClosedMu.Lock()
		isClosed := conn.isClosed
		if !isClosed {
			// Dispatch the packet to the corresponding connection's input channel
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
				packet.AddChannel("pcpConn.InputChannel")
			}
			conn.inputChannel <- packet
			conn.isClosedMu.Unlock()
			return
		}
		conn.isClosedMu.Unlock()
	}

	// then check if the connection exists in temp connection map
	s.mu.Lock()
	tempConn, ok := s.tempConnMap[connKey]
	s.mu.Unlock()
	if ok && !tempConn.isClosed { // no need to use mutex since connection is not ready yet
		// Dispatch the packet to the corresponding connection's input channel
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
			packet.AddChannel("pcpConn.InputChannel")
		}
		tempConn.inputChannel <- packet
		return
	}

	log.Printf("Received non-SYN packet for non-existent or closed connection: %s\n", connKey)
	if rp.Debug && packet.GetChunkReference() != nil {
		packet.TickFootPrint(fp)
	}
	packet.ReturnChunk()
}

// handleSynPacket handles a SYN packet and initiates a new connection.
func (s *Service) handleSynPacket(packet *PcpPacket) {
	// Extract source IP address and port from the packet
	sourceAddr := packet.SrcAddr
	sourcePort := packet.SourcePort

	// Create connection key
	connKey := fmt.Sprintf("%s:%d", sourceAddr.(*net.IPAddr).IP.To4().String(), sourcePort)

	// Check if the connection already exists in the connection map
	s.mu.Lock()
	_, ok := s.connectionMap[connKey]
	s.mu.Unlock()
	if ok {
		log.Printf("Received SYN packet for existing connection: %s. Ignore it.\n", connKey)
		return
	}

	// then check if the connection exists in temp connection map
	s.mu.Lock()
	_, ok = s.tempConnMap[connKey]
	s.mu.Unlock()
	if ok {
		log.Printf("Received SYN packet for existing temp connection: %s. Ignore it.\n", connKey)
		return
	}

	// Create a new temporary connection object for the 3-way handshake
	connParams := &connectionParams{
		key:                      connKey,
		isServer:                 true,
		remoteAddr:               sourceAddr,
		remotePort:               int(sourcePort),
		localAddr:                s.serviceAddr,
		localPort:                s.port,
		outputChan:               s.outputChan,
		sigOutputChan:            s.sigOutputChan,
		connCloseSignalChan:      s.connCloseSignal,
		newConnChannel:           s.newConnChannel,
		connSignalFailedToParent: s.connSignalFailed,
	}
	//newConn, err := NewConnection(connKey, true, sourceAddr, int(sourcePort), s.ServiceAddr, s.Port, s.OutputChan, s.sigOutputChan, s.ConnCloseSignal, s.newConnChannel, s.connSignalFailed)
	newConn, err := newConnection(connParams, s.connConfig)
	if err != nil {
		log.Printf("Error creating new connection for %s: %s\n", connKey, err)
		return
	}

	newConn.initServerState = SynReceived

	// Add the new connection to the temporary connection map
	s.mu.Lock()
	s.tempConnMap[connKey] = newConn
	s.mu.Unlock()

	// TCP options support
	// MSS support negotiation
	if packet.TcpOptions.mss > 0 {
		if packet.TcpOptions.mss < newConn.tcpOptions.mss {
			newConn.tcpOptions.mss = packet.TcpOptions.mss // default value is config.PreferredMSS
		}
	} else {
		newConn.tcpOptions.mss = 0 // disble MSS
	}

	// Window Scaling support
	if packet.TcpOptions.windowScaleShiftCount == 0 {
		newConn.tcpOptions.windowScaleShiftCount = 0 // Disable it
	}

	// SACK support
	newConn.tcpOptions.permitSack = newConn.tcpOptions.permitSack && packet.TcpOptions.permitSack    // both sides need to permit SACK
	newConn.tcpOptions.SackEnabled = newConn.tcpOptions.permitSack && newConn.tcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

	// timestamp support
	newConn.tcpOptions.timestampEnabled = packet.TcpOptions.timestampEnabled
	newConn.tcpOptions.tsEchoReplyValue = packet.TcpOptions.timestamp

	// start the temp connection's goroutine to handle 3-way handshaking process
	newConn.wg.Add(1)
	go newConn.handle3WayHandshake()

	// Send SYN-ACK packet to the SYN packet sender
	newConn.lastAckNumber = SeqIncrement(packet.SequenceNumber)
	newConn.initialPeerSeq = packet.SequenceNumber
	newConn.initSendSynAck()
	newConn.startConnSignalTimer()
	newConn.nextSequenceNumber = SeqIncrement(newConn.nextSequenceNumber) // implicit modulo op

	log.Printf("Sent SYN-ACK packet to: %s\n", connKey)
}

// handle close connection request from ClientConnection
func (s *Service) handleCloseConnections() {
	// Decrease WaitGroup counter when the goroutine completes
	defer s.wg.Done()

	for {
		var conn *Connection
		select {
		case <-s.closeSignal:
			log.Println("Closing service handleCloseConnections go routine")
			// Close the handleServicePackets goroutine to gracefully shutdown
			return
		case conn = <-s.connSignalFailed:
			// clear it from p.ConnectionMap
			s.mu.Lock()
			_, ok := s.connectionMap[conn.params.key] // just make sure it really in ConnectionMap for debug purpose
			s.mu.Unlock()
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
				continue
			}
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
			return
		case conn = <-s.connCloseSignal:
			// clear it from p.ConnectionMap
			s.mu.Lock()
			_, ok := s.connectionMap[conn.params.key] // just make sure it really in ConnectionMap for debug purpose
			s.mu.Unlock()
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp connection does not exist in %s:%d->%s:%d", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
				continue
			}

			// delete the clientConn from ConnectionMap
			s.mu.Lock()
			delete(s.connectionMap, conn.params.key)
			s.mu.Unlock()
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
		}

	}
}

func (s *Service) Close() error {
	if s.isClosed {
		return nil
	}
	// begin close the service itself and clear resources
	s.isClosed = true

	log.Println("Beginning PCP service shutdown...")
	// Close all connections associated with this service
	var openConns []*Connection
	s.mu.Lock()
	for _, conn := range s.connectionMap {
		openConns = append(openConns, conn)
	}
	s.mu.Unlock()
	wg := sync.WaitGroup{}
	for _, conn := range openConns {
		if conn != nil {
			wg.Add(1)
			go conn.CloseAsGoRoutine(&wg)
		}
	}
	s.mu.Lock()
	s.connectionMap = nil
	s.mu.Unlock()
	log.Println("PCP service: all open connections closed")

	wg.Wait() // wait for connections to close
	var tempConns []*Connection
	// Close all temp connections associated with this service
	s.mu.Lock()
	for _, tempConn := range s.tempConnMap {
		tempConns = append(tempConns, tempConn)
	}
	s.mu.Unlock()
	for _, tempConn := range tempConns {
		if tempConn != nil {
			close(tempConn.connSignalFailed)
		}
	}
	s.mu.Lock()
	s.tempConnMap = nil
	s.mu.Unlock()
	log.Println("PCP service: all temp connections closed")

	// wait for 500ms to allow temp connections to finish closing
	SleepForMs(100)

	// send signal to service go routines to gracefully close them
	close(s.closeSignal)

	s.wg.Wait()

	// close channels created by the service
	close(s.inputChannel)
	close(s.newConnChannel)
	close(s.connCloseSignal)
	close(s.connSignalFailed)

	log.Println("PCP Service: resource cleared.")
	// send signal to parent pcpProtocolConnection to clear service resource
	s.serviceCloseSignal <- s
	log.Println("PCP Service: signal sent to parent PCP service to remove service entry.")

	// remove the iptable rules for the service
	err := removeIptablesRule(s.serviceAddr.(*net.IPAddr).IP.String(), s.port)
	if err != nil {
		log.Printf("PCP Service: Error removing iptable rules for service %s:%d: %s", s.serviceAddr.(*net.IPAddr).IP.String(), s.port, err)
		return err
	}

	log.Printf("PCP Service %s:%d shut down successfully.\n", s.serviceAddr.(*net.IPAddr).IP.String(), s.port)

	return nil
}
