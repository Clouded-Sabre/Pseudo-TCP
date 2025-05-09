package lib

import (
	"encoding/binary"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	rs "github.com/Clouded-Sabre/rawsocket/lib"
	rp "github.com/Clouded-Sabre/ringpool/lib"
)

// pcp protocol connection struct
type PcpProtocolConnection struct {
	//static
	config                *PcpProtocolConnConfig
	key                   string      // PCP protocol connection's key
	protocolId            int         // protocol id
	isServer              bool        // Role of the object
	serverAddr, localAddr *net.IPAddr // server and local ip address
	//ipConn                *net.IPConn // net.IPConn connection
	rsConn  rs.RawConnection // RawSocketConn connection
	pcpCore *PcpCore         // PcpCore object

	// variables
	outputChan, sigOutputChan chan *PcpPacket             // output channel for normal packets and signalling packets respectively
	connectionMap             map[string]*Connection      // used for client side only
	tempConnectionMap         map[string]*Connection      // used for client side only
	serviceMap                map[int]*Service            // used for server side only
	connCloseSignal           chan *Connection            // receiving close signal from pcp connection. used for client side only
	serviceCloseSignal        chan *Service               // receiving close signal from pcp service. used for service side only
	pConnCloseSignal          chan *PcpProtocolConnection // used to send signal to parent service to tell it to clear this connection from its map
	closeSignal               chan struct{}               // used to send close signal to HandleIncomingPackets, handleOutgoingPackets, handleCloseConnection go routines to stop when timeout arrives
	emptyMapTimer             *time.Timer                 // if this pcpProtocolConnection has no connection for 10 seconds, close it. used for client side only
	wg                        sync.WaitGroup              // WaitGroup to synchronize goroutines
	//iptableRules              []int                       // slice of port number on server address which has iptables rules. used for client side only
	isClosed      bool       // to prevent calling close multiple time
	mu            sync.Mutex // to protect maps access
	localPortPool *PortPool  // a pool of avaibable local port numbers for local port number allocation
}

type PcpProtocolConnConfig struct {
	IptableRuleDaley                 int
	PreferredMSS                     int
	PacketLostSimulation             bool
	PConnTimeout                     int
	ClientPortUpper, ClientPortLower int
	ConnConfig                       *ConnectionConfig
	VerifyChecksum                   bool
	PConnOutputQueue                 int
}

func DefaultPcpProtocolConnConfig() *PcpProtocolConnConfig {
	return &PcpProtocolConnConfig{
		IptableRuleDaley:     200,
		PreferredMSS:         1440, // Maximum Segment Size
		PacketLostSimulation: false,
		PConnTimeout:         10, // 10 seconds
		ClientPortUpper:      65535,
		ClientPortLower:      49152,
		ConnConfig:           DefaultConnectionConfig(),
		VerifyChecksum:       false, // Checksum verification is turned off by default because TCP checksum offload is widely used nowadays
		PConnOutputQueue:     100,
	}
}

func newPcpProtocolConnection(pcpCore *PcpCore, key string, isServer bool, protocolId int, serverAddr, localAddr *net.IPAddr, pConnCloseSignal chan *PcpProtocolConnection, config *PcpProtocolConnConfig) (*PcpProtocolConnection, error) {
	var (
		//ipConn *net.IPConn
		rsConn rs.RawConnection
		err    error
	)

	if isServer {
		// Listen on the PCP protocol (20) at the server IP
		//ipConn, err = net.ListenIP("ip:"+strconv.Itoa(protocolId), serverAddr)
		rsConn, err = (*pcpCore.rscore).ListenIP("ip:"+strconv.Itoa(protocolId), serverAddr)
		if err != nil {
			fmt.Println("Error listening:", err)
			return nil, err
		}
	} else { // client
		// dial in PCP protocol (20) to the server IP
		//ipConn, err = net.DialIP("ip:"+strconv.Itoa(protocolId), localAddr, serverAddr)
		rsConn, err = (*pcpCore.rscore).DialIP("ip:"+strconv.Itoa(protocolId), localAddr, serverAddr)
		if err != nil {
			fmt.Println("Error dialing:", err)
			return nil, err
		}
	}

	pConnection := &PcpProtocolConnection{
		key:        key,
		isServer:   isServer,
		protocolId: protocolId,
		localAddr:  localAddr,
		serverAddr: serverAddr,
		//ipConn:             ipConn,
		rsConn:             rsConn,
		pcpCore:            pcpCore,
		outputChan:         make(chan *PcpPacket, config.PConnOutputQueue),
		sigOutputChan:      make(chan *PcpPacket, 10),
		connectionMap:      make(map[string]*Connection),
		tempConnectionMap:  make(map[string]*Connection),
		connCloseSignal:    make(chan *Connection),
		serviceCloseSignal: make(chan *Service),
		serviceMap:         make(map[int]*Service),

		pConnCloseSignal: pConnCloseSignal,
		closeSignal:      make(chan struct{}),
		wg:               sync.WaitGroup{},
		//iptableRules:     make([]int, 0),
		config:        config,
		mu:            sync.Mutex{},
		localPortPool: newPortPool(minPort, maxPort),
	}

	// Start goroutines
	pConnection.wg.Add(3) // Increase WaitGroup counter by 3 for the three goroutines
	go pConnection.handleIncomingPackets()
	go pConnection.handleOutgoingPackets()
	go pConnection.handleCloseConnection()

	return pConnection, nil
}

func (p *PcpProtocolConnection) dial(serverPort int, connConfig *ConnectionConfig) (*Connection, error) {
	log.Println("PcpProtocolConnection.dial: connConfig is", connConfig)
	// Choose a random client port
	clientPort, err := p.localPortPool.allocatePort()
	if err != nil {
		log.Fatalln("PcpProtocolConnection.dial:", err)
	}

	// Create connection key
	connKey := fmt.Sprintf("%d:%d", clientPort, serverPort)

	connParam := &connectionParams{
		key:                      connKey,
		isServer:                 false,
		remoteAddr:               p.serverAddr,
		remotePort:               int(serverPort),
		localAddr:                p.localAddr,
		localPort:                clientPort,
		outputChan:               p.outputChan,
		sigOutputChan:            p.sigOutputChan,
		connCloseSignalChan:      p.connCloseSignal,
		newConnChannel:           nil,
		connSignalFailedToParent: nil,
		pcpCore:                  p.pcpCore,
	}

	// Create a new temporary connection object for the 3-way handshake
	newConn, err := newConnection(connParam, connConfig)
	if err != nil {
		fmt.Printf("PcpProtocolConnection.dial: Error creating new connection to %s:%d because of error: %s\n", p.serverAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.mu.Lock()
	p.tempConnectionMap[connKey] = newConn
	p.mu.Unlock()

	// Add filtering rule to drop RST packets
	if err := (*p.pcpCore.filter).AddTcpClientFiltering(connParam.remoteAddr.(*net.IPAddr).IP.String(), serverPort); err != nil {
		log.Println("PcpProtocolConnection.dial: Error adding filtering rule:", err)
		return nil, err
	}
	//p.iptableRules = append(p.iptableRules, serverPort) // record it for later deletion of the rules when connection closes

	// sleep for 200ms to make sure filtering rule takes effect
	SleepForMs(p.config.IptableRuleDaley)

	// Send SYN to server
	newConn.initSendSyn()
	newConn.startConnSignalTimer()
	newConn.nextSequenceNumber = SeqIncrement(newConn.nextSequenceNumber) // implicit modulo op
	log.Println("PcpProtocolConnection.dial: Initiated connection to server with connKey:", connKey)

	// Wait for SYN-ACK
	// Create a loop to read from connection's input channel till we see SYN-ACK packet from the other end
	for {
		select {
		case <-newConn.connSignalFailed:
			// dial action failed
			// Connection dialing failed, remove newConn from tempClientConnections
			newConn.isClosed = true // no need to use mutex since connection is not ready yet
			p.mu.Lock()
			delete(p.tempConnectionMap, connKey)
			p.mu.Unlock()

			if newConn.connSignalTimer != nil {
				newConn.stopConnSignalTimer()
				newConn.connSignalTimer = nil
			}
			err = fmt.Errorf("PcpProtocolConnection.dial: dialing PCP connection failed due to timeout")
			log.Println(err)
			return nil, err

		case packet := <-newConn.inputChannel:
			if packet.Flags == SYNFlag|ACKFlag { // Verify if it's a SYN-ACK from the server
				newConn.stopConnSignalTimer() // stops the connection signal resend timer
				newConn.initClientState = SynAckReceived

				// MSS support negotiation
				if packet.TcpOptions.mss > 0 {
					log.Printf("PcpService.handleSynPacket: Received MSS is %d while newConn.tcpOptions.mss is %d", packet.TcpOptions.mss, newConn.tcpOptions.mss)
					if packet.TcpOptions.mss < newConn.tcpOptions.mss {
						newConn.tcpOptions.mss = packet.TcpOptions.mss // default value is config.PreferredMSS
					}
					log.Printf("Negotiated mss is %s%d%s", Red, newConn.tcpOptions.mss, Reset)
				} else {
					newConn.tcpOptions.mss = 0 // disble MSS
				}

				// Prepare ACK packet
				newConn.lastAckNumber = SeqIncrement(packet.SequenceNumber)
				newConn.initialPeerSeq = packet.SequenceNumber
				newConn.initSendAck()

				// Connection established, remove newConn from tempClientConnections, and place it into clientConnections pool
				p.mu.Lock()
				delete(p.tempConnectionMap, connKey)
				p.mu.Unlock()

				newConn.isOpenConnection = true
				newConn.tcpOptions.timestampEnabled = packet.TcpOptions.timestampEnabled
				// Sack permit and SackOption support
				newConn.tcpOptions.permitSack = newConn.tcpOptions.permitSack && packet.TcpOptions.permitSack    // both sides need to permit SACK
				newConn.tcpOptions.SackEnabled = newConn.tcpOptions.permitSack && newConn.tcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

				p.mu.Lock()
				p.connectionMap[connKey] = newConn
				p.mu.Unlock()

				// start go routine to handle incoming packets for new connection
				newConn.wg.Add(1)
				go newConn.handleIncomingPackets()
				// start ResendTimer if Sack Enabled
				if newConn.tcpOptions.SackEnabled {
					// Start the resend timer
					newConn.startResendTimer()
				}

				newConn.initClientState = 0 // reset it to zero so that later ACK message can be processed correctly

				return newConn, nil
			}
		}

	}
}

func (p *PcpProtocolConnection) clientProcessingIncomingPacket(buffer []byte) {
	var (
		err   error
		n, fp int
	)

	// Set a read deadline to ensure non-blocking behavior
	p.rsConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, err = p.rsConn.Read(buffer)
	if err != nil {
		// Check if the error is a timeout
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// Handle timeout error (no data received within the timeout period)
			return // Continue waiting for incoming packets or handling closeSignal
		}

		// other errors
		log.Println("pcpProtocolConnection.handleIncomingPackets:Error reading:", err)
		return
	}

	//log.Println("The received PCP segment's total length is", n)
	//log.Printf("PcpProtocolConnection.clientProcessingIncomingPacket: Source IP: %s, Destination IP: %s", srcIP, dstIP)

	index, err := ExtractIpPayload(buffer[:n])
	if err != nil {
		log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: Received IP frame is il-formated. Ignore it!", err)
		return
	}

	// check PCP packet checksum
	// please note the first TcpPseudoHeaderLength bytes are reseved for Tcp pseudo header
	if p.config.VerifyChecksum {
		if !VerifyChecksum(buffer[index-TcpPseudoHeaderLength:n], p.serverAddr, p.localAddr, uint8(p.protocolId)) {
			log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: Packet checksum verification failed. Skip this packet.")
			return
		}
	}

	// Extract destination port
	pcpFrame := buffer[index:n]

	// Extract source and destination ports from the PCP segment
	if n-index < 4 {
		log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: TCP/PCP segment too short")
		return
	}

	sourcePort := binary.BigEndian.Uint16(pcpFrame[:2])
	destPort := binary.BigEndian.Uint16(pcpFrame[2:4])

	// Create connection key. Since it's incoming packet
	// source and destination port needs to be reversed when calculating connection key
	connKey := fmt.Sprintf("%d:%d", destPort, sourcePort)
	//log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: Got packet from", srcIP, "port", sourcePort, "to", dstIP, "port", destPort, "connection key:", connKey)

	// First check if the connection exists in the connection map
	p.mu.Lock()
	conn, ok := p.connectionMap[connKey]
	p.mu.Unlock()

	if ok {
		conn.isClosedMu.Lock()
		isClosed := conn.isClosed
		if !isClosed {
			packet := &PcpPacket{}
			err = packet.Unmarshal(pcpFrame, p.serverAddr, p.localAddr)
			if err != nil {
				log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: Received TCP frame is il-formated. Ignore it!", err)
				return
			}

			if rp.Debug && packet.GetChunkReference() != nil {
				fp = packet.AddFootPrint("pcpProtocolConn.handleIncomingPackets")
			}
			// open connection. Dispatch the packet to the corresponding connection's input channel
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
				packet.AddChannel("Conn.InputChannel")
			}
			conn.inputChannel <- packet

			conn.isClosedMu.Unlock()
			return
		}
		conn.isClosedMu.Unlock()
	}

	// then check if packet belongs to an temp connection.
	p.mu.Lock()
	tempConn, ok := p.tempConnectionMap[connKey]
	p.mu.Unlock()
	if ok && !tempConn.isClosed { // no need to use mutex since connection is not ready yet
		packet := &PcpPacket{}
		err = packet.Unmarshal(pcpFrame, p.serverAddr, p.localAddr)
		if err != nil {
			log.Println("PcpProtocolConnection.clientProcessingIncomingPacket: Received TCP frame is il-formated. Ignore it!", err)
			return
		}

		if rp.Debug && packet.GetChunkReference() != nil {
			fp = packet.AddFootPrint("pcpProtocolConn.handleIncomingPackets")
		}
		if len(packet.Payload) == 0 && packet.AcknowledgmentNum-tempConn.initialSeq < 2 {
			// forward to that connection's input channel
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
				packet.AddChannel("TempConn.InputChannel")
			}
			tempConn.inputChannel <- packet
			return
		} else if len(packet.Payload) > 0 { // data packet
			// since the connection is not ready yet, discard the data packet for the time being
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
			}
			packet.ReturnChunk()
			return
		}
	}

	fmt.Printf("PcpProtocolConnection.clientProcessingIncomingPacket: Received data packet for non-existent or closed connection: %s\n", connKey)
}

func (p *PcpProtocolConnection) serverProcessingIncomingPacket(buffer []byte) {
	var fp int

	pcpFrame := buffer[TcpPseudoHeaderLength:] // the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp pseudo header
	// Set a read deadline to ensure non-blocking behavior
	p.rsConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, addr, err := p.rsConn.ReadFrom(pcpFrame)
	if err != nil {
		// Check if the error is a timeout
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// Handle timeout error (no data received within the timeout period)
			return // Continue waiting for incoming packets or handling closeSignal
		}

		log.Println("PcpProtocolConnection.handlingIncomingPackets:Error reading:", err)
		return
	}

	if p.pcpCore.config.Debug {
		log.Printf("Server received packet: len=%d first16=%x", n, pcpFrame[:min(16, n)])
	}

	// Use the copy for all subsequent processing
	if p.config.VerifyChecksum {
		if !VerifyChecksum(buffer[:TcpPseudoHeaderLength+n], addr, p.serverAddr, uint8(p.protocolId)) {
			log.Printf("PcpProtocolConnection.serverProcessingIncomingPacket: Packet from %s checksum verification failed. Skip this packet(length %d).\n", addr.(*net.IPAddr).String(), n)
			return
		}
	}

	// Extract destination port
	packet := &PcpPacket{}
	err = packet.Unmarshal(pcpFrame[:n], addr, p.serverAddr)
	if err != nil {
		if p.pcpCore.config.Debug {
			log.Printf("PcpProtocolConnection.serverProcessingIncomingPacket: PCP packet from %s unmarshal error: %s Ignore the packet!\n", addr.(*net.IPAddr).String(), err)
		}
		// don't need to return chunk because it is done in copyToPayload
		return
	}

	if rp.Debug && packet.GetChunkReference() != nil {
		fp = packet.AddFootPrint("pcpProtocolConn.serverProcessingIncomingPacket")
	}

	destPort := packet.DestinationPort
	//log.Printf("Got packet with options: %+v\n", packet.TcpOptions)

	// Check if a connection is registered for the packet
	p.mu.Lock()
	service, ok := p.serviceMap[int(destPort)]
	p.mu.Unlock()

	if !ok {
		//fmt.Println("No service registered for port:", destPort)
		// return the packet's chunk
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
		}
		packet.ReturnChunk()
		return
	}

	if service.isClosed && packet.GetChunkReference() != nil {
		log.Println("PcpProtocolConnection.serverProcessingIncomingPacket: Packet is destined to a closed service. Ignore it.")
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
		}
		packet.ReturnChunk()
		return
	}

	// Dispatch the packet to the corresponding service's input channel
	if rp.Debug && packet.GetChunkReference() != nil {
		packet.TickFootPrint(fp)
		packet.AddChannel("Service.InputChannel")
	}
	service.inputChannel <- packet
}

// handleIncomingPackets is the main service packet dispatches loop.
func (p *PcpProtocolConnection) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	// the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp Pseudo Header
	/*var buffer []byte
	if p.isServer {
		buffer = make([]byte, p.config.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
		if p.pcpCore.config.Debug {
			log.Printf("Server created receive buffer %p", buffer)
		}
	} else {
		buffer = make([]byte, p.config.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+IpHeaderMaxLength+TcpPseudoHeaderLength)
	}*/
	buffer := make([]byte, bufferLength) // buffer length is 65536 bytes to be big engough for max TCP segment length

	// main loop for incoming packets
	for {
		select {
		case <-p.closeSignal:
			return
		default:
			if p.isServer {
				p.serverProcessingIncomingPacket(buffer)
			} else {
				p.clientProcessingIncomingPacket(buffer)
			}
		}
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *PcpProtocolConnection) handleOutgoingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	var (
		count      = 0
		lostCount  = 0
		frameBytes = make([]byte, p.config.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
		n          = 0
		fp         int
		err        error
		packet     *PcpPacket
	)
	packetLost := false
	for {
		select {
		case <-p.closeSignal:
			return
		case packet = <-p.sigOutputChan:
		default:
			select {
			case <-p.closeSignal:
				return
			case packet = <-p.sigOutputChan:
			case packet = <-p.outputChan: // Subscribe to p.OutputChan
			}
		}

		if rp.Debug && packet.GetChunkReference() != nil {
			err := packet.TickChannel()
			if err != nil {
				log.Println("PCPProtocolConnection.handleOutgoingPackets:", err)
			}
			fp = packet.AddFootPrint("pcpProtocolConnection.handleOutgoingPackets")
		}

		// Packet loss simulation
		if p.config.PacketLostSimulation {
			if count == 0 {
				lostCount = rand.Intn(10)
			}
			if count == lostCount {
				log.Println("Packet", count, "is lost")
				lostCount = 100
				packetLost = true
			}
		}

		if !packetLost {
			// Marshal the packet into bytes
			n, err = packet.Marshal(uint8(p.protocolId), frameBytes)
			if err != nil {
				fmt.Println("PCPProtocolConnection.handleOutgoingPackets: Error marshalling packet:", err)
				log.Fatal()
			}
			// Write the packet to the interface
			frame := frameBytes[TcpPseudoHeaderLength:] // first part of framesBytes is actually Tcp Pseudo Header
			if p.isServer {
				//_, err = p.ipConn.WriteTo(frame[:n], packet.DestAddr)
				_, err = p.rsConn.WriteTo(frame[:n], packet.DestAddr)
			} else {
				//_, err = p.ipConn.Write(frame[:n])
				_, err = p.rsConn.Write(frame[:n])
			}
			if err != nil {
				log.Println("PCPProtocolConnection.handleOutgoingPackets: Error writing packet:", err, "Skip this packet.")
			}
		}

		// Always run packet loss simulation before handling the packet
		if p.config.PacketLostSimulation {
			count = (count + 1) % 10
			packetLost = false
		}

		// Return chunk to pool if:
		// 1. Retransmission is disabled, or
		// 2. It's not a data packet, or
		// 3. SACK is not enabled, or
		// 4. It's a keepalive message
		if !packet.Conn.config.RetransmissionEnabled ||
			len(packet.Payload) == 0 ||
			!packet.Conn.tcpOptions.SackEnabled ||
			packet.IsKeepAliveMassege {
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
			}
			packet.ReturnChunk()
			continue
		}

		// Only add to ResendPackets if retransmission is enabled and packet not already in ResendPackets
		if _, found := packet.Conn.resendPackets.GetSentPacket(packet.SequenceNumber); !found {
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
			}
			packet.Conn.resendPackets.AddSentPacket(packet)
		} else {
			if p.config.ConnConfig.Debug {
				fmt.Println("PCPProtocolConnection.handleOutgoingPackets: this is a resent packet. Do not put it into ResendPackets")
			}
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
			}
			packet.ReturnChunk()
		}
	}
}

// handle close connection request from ClientConnection
func (p *PcpProtocolConnection) handleCloseConnection() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return
		case conn := <-p.connCloseSignal:
			// clear it from p.ConnectionMap
			p.mu.Lock()
			_, ok := p.connectionMap[conn.params.key]
			p.mu.Unlock()
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("PcpProtocolConnection.handleCloseConnection: Pcp Client connection does not exist in %s:%d->%s:%d", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
				continue
			}

			// delete the clientConn from ConnectionMap
			p.mu.Lock()
			delete(p.connectionMap, conn.params.key)
			p.mu.Unlock()
			// return local port number to port pool
			if !conn.params.isServer { // only happens on client side
				err := p.localPortPool.returnPort(conn.params.localPort)
				if err != nil {
					// should never happen
					log.Fatalf("PcpProtocolConnection.handleCloseConnection: %s\n", err)
				}
			}

			log.Printf("PcpProtocolConnection.handleCloseConnection: Pcp connection %s:%d->%s:%d terminated and removed.", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)

			// if pcpProtocolConnection does not have any connection for 10 seconds, close it
			// Start or reset the timer if ConnectionMap becomes empty
			p.mu.Lock()
			length := len(p.connectionMap)
			p.mu.Unlock()
			if length == 0 {
				// Stop the previous timer if it exists
				if p.emptyMapTimer != nil {
					p.emptyMapTimer.Stop()
				}

				// Start a new timer for 10 seconds
				log.Println("PcpProtocolConnection.handleCloseConnection: Wait for", p.config.PConnTimeout, "seconds before closing the PCP protocol connection")
				p.emptyMapTimer = time.AfterFunc(time.Duration(p.config.PConnTimeout)*time.Second, func() {
					// Close the connection by sending closeSignal to all goroutines and clear resources
					if p.emptyMapTimer != nil {
						p.emptyMapTimer.Stop()
						p.mu.Lock()
						length := len(p.connectionMap)
						p.mu.Unlock()
						if length == 0 {
							p.Close()
						}
					}
				})
			}
		case srv := <-p.serviceCloseSignal:
			// clear it from p.serviceMap
			p.mu.Lock()
			_, ok := p.serviceMap[srv.port] // just make sure it really in serviceMap for debug purpose
			p.mu.Unlock()
			if !ok {
				// Service does not exist in serviceMap
				log.Printf("PcpProtocolConnection.handleCloseConnection: Pcp Service %s:%d does not exist in service map.\n", srv.serviceAddr.(*net.IPAddr).IP.String(), srv.port)
				continue
			}

			// delete the service from serviceMap
			p.mu.Lock()
			delete(p.serviceMap, srv.port)
			p.mu.Unlock()
			log.Printf("PcpProtocolConnection.handleCloseConnection: Pcp service %s:%d stopped.", srv.serviceAddr.(*net.IPAddr).IP.String(), srv.port)
		}
	}
}

// Function to close the connection gracefully
func (p *PcpProtocolConnection) Close() {
	if p.isClosed {
		return
	}
	p.isClosed = true

	// Close all PCP Connections
	log.Println("PcpProtocolConnection: begin to close...")
	var connectionsToRemove []*Connection
	p.mu.Lock()
	for _, conn := range p.connectionMap {
		connectionsToRemove = append(connectionsToRemove, conn)
	}
	p.mu.Unlock()
	wg := sync.WaitGroup{}
	for _, conn := range connectionsToRemove {
		wg.Add(1)
		go conn.CloseAsGoRoutine(&wg)
	}
	p.mu.Lock()
	p.connectionMap = nil // Clear the map after closing all connections
	p.mu.Unlock()
	wg.Wait() // wait for all pcp connections to close
	log.Println("PcpProtocolConnection: All Pcp Connections of PcpProtocolConnection closed...")

	// Close all connections associated with this service
	var svcsToRemove []*Service
	p.mu.Lock()
	for _, svc := range p.serviceMap {
		svcsToRemove = append(svcsToRemove, svc)
	}
	p.mu.Unlock()
	for _, svc := range svcsToRemove {
		svc.Close()
	}
	p.mu.Lock()
	p.serviceMap = nil // Clear the map after closing all services
	p.mu.Unlock()
	log.Println("PcpProtocolConnection: All Pcp services of PcpProtocolConnection closed...")

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	log.Println("PcpProtocolConnection: Waiting for go routines to close")
	p.wg.Wait()
	log.Println("PcpProtocolConnection: Go routines closed")

	// Clear resources

	//p.ipConn.Close() //close protocol connection
	p.rsConn.Close() //close protocol connection

	if p.emptyMapTimer != nil {
		p.emptyMapTimer.Stop()
		p.emptyMapTimer = nil
	}

	close(p.sigOutputChan)
	close(p.outputChan)
	close(p.connCloseSignal)
	close(p.serviceCloseSignal)

	// sent pConn close signal to parent pClient
	p.pConnCloseSignal <- p

	log.Println("PcpProtocolConnection closed gracefully.")
}
