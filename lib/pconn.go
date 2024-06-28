package lib

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	rp "github.com/Clouded-Sabre/ringpool/lib"
)

// pcp protocol connection struct
type PcpProtocolConnection struct {
	//static
	config                *pcpProtocolConnConfig
	key                   string         // PCP protocol connection's key
	protocolId            int            // protocol id
	isServer              bool           // Role of the object
	serverAddr, localAddr *net.IPAddr    // server and local ip address
	clientConn            *net.IPConn    // used for client side only
	serverConn            net.PacketConn // used for server side only
	// variables
	outputChan, sigOutputChan chan *PcpPacket             // output channel for normal packets and signalling packets respectively
	connectionMap             map[string]*Connection      // used for client side only
	tempConnectionMap         map[string]*Connection      // used for client side only
	connCloseSignal           chan *Connection            // receiving close signal from pcp connection. used for client side only
	serviceMap                map[int]*Service            // used for server side only
	serviceCloseSignal        chan *Service               // receiving close signal from pcp service. used for service side only
	pConnCloseSignal          chan *PcpProtocolConnection // used to send signal to parent service to tell it to clear this connection from its map
	closeSignal               chan struct{}               // used to send close signal to HandleIncomingPackets, handleOutgoingPackets, handleCloseConnection go routines to stop when timeout arrives
	emptyMapTimer             *time.Timer                 // if this pcpProtocolConnection has no connection for 10 seconds, close it. used for client side only
	wg                        sync.WaitGroup              // WaitGroup to synchronize goroutines
	iptableRules              []int                       // slice of port number on server address which has iptables rules. used for client side only
}

type pcpProtocolConnConfig struct {
	iptableRuleDaley                 int
	preferredMSS                     int
	packetLostSimulation             bool
	pConnTimeout                     int
	clientPortUpper, clientPortLower int
	connConfig                       *connectionConfig
}

func newPcpProtocolConnConfig(pcpConfig *config.Config) *pcpProtocolConnConfig {
	return &pcpProtocolConnConfig{
		iptableRuleDaley:     pcpConfig.IptableRuleDaley,
		preferredMSS:         pcpConfig.PreferredMSS,
		packetLostSimulation: pcpConfig.PacketLostSimulation,
		pConnTimeout:         pcpConfig.PConnTimeout,
		clientPortUpper:      pcpConfig.ClientPortUpper,
		clientPortLower:      pcpConfig.ClientPortLower,
		connConfig:           newConnectionConfig(pcpConfig),
	}
}

func newPcpProtocolConnection(key string, isServer bool, protocolId int, serverAddr, localAddr *net.IPAddr, pConnCloseSignal chan *PcpProtocolConnection, config *pcpProtocolConnConfig) (*PcpProtocolConnection, error) {
	var (
		serverConn net.PacketConn
		clientConn *net.IPConn
		err        error
	)

	if isServer {
		// Listen on the PCP protocol (20) at the server IP
		serverConn, err = net.ListenPacket("ip:"+strconv.Itoa(protocolId), serverAddr.String())
		if err != nil {
			fmt.Println("Error listening:", err)
			return nil, err
		}
	} else { // client
		// dial in PCP protocol (20) to the server IP
		clientConn, err = net.DialIP("ip:"+strconv.Itoa(protocolId), localAddr, serverAddr)
		if err != nil {
			fmt.Println("Error listening:", err)
			return nil, err
		}
	}

	pConnection := &PcpProtocolConnection{
		key:                key,
		isServer:           isServer,
		protocolId:         protocolId,
		localAddr:          localAddr,
		serverAddr:         serverAddr,
		clientConn:         clientConn,
		serverConn:         serverConn,
		outputChan:         make(chan *PcpPacket),
		sigOutputChan:      make(chan *PcpPacket),
		connectionMap:      make(map[string]*Connection),
		tempConnectionMap:  make(map[string]*Connection),
		connCloseSignal:    make(chan *Connection),
		serviceCloseSignal: make(chan *Service),
		serviceMap:         make(map[int]*Service),

		pConnCloseSignal: pConnCloseSignal,
		closeSignal:      make(chan struct{}),
		wg:               sync.WaitGroup{},
		iptableRules:     make([]int, 0),
		config:           config,
	}

	// Start goroutines
	pConnection.wg.Add(3) // Increase WaitGroup counter by 3 for the three goroutines
	go pConnection.handleIncomingPackets()
	go pConnection.handleOutgoingPackets()
	go pConnection.handleCloseConnection()

	return pConnection, nil
}

func (p *PcpProtocolConnection) dial(serverPort int, connConfig *connectionConfig) (*Connection, error) {
	// Choose a random client port
	clientPort := p.getAvailableRandomClientPort()

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
	}

	// Create a new temporary connection object for the 3-way handshake
	//newConn, err := NewConnection(connKey, false, p.ServerAddr, int(serverPort), p.LocalAddr, clientPort, p.OutputChan, p.sigOutputChan, p.ConnCloseSignal, nil, nil)
	newConn, err := newConnection(connParam, connConfig)
	if err != nil {
		fmt.Printf("Error creating new connection to %s:%d because of error: %s\n", p.serverAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.tempConnectionMap[connKey] = newConn

	// Add iptables rule to drop RST packets
	if err := addIptablesRule(p.serverAddr.IP.To4().String(), serverPort); err != nil {
		log.Println("Error adding iptables rule:", err)
		return nil, err
	}
	p.iptableRules = append(p.iptableRules, serverPort) // record it for later deletion of the rules when connection closes

	// sleep for 200ms to make sure iptable rule takes effect
	SleepForMs(p.config.iptableRuleDaley)

	// Send SYN to server
	newConn.initSendSyn()
	newConn.startConnSignalTimer()
	newConn.nextSequenceNumber = SeqIncrement(newConn.nextSequenceNumber) // implicit modulo op
	log.Println("Initiated connection to server with connKey:", connKey)

	// Wait for SYN-ACK
	// Create a loop to read from connection's input channel till we see SYN-ACK packet from the other end
	for {
		select {
		case <-newConn.connSignalFailed:
			// dial action failed
			// Connection dialing failed, remove newConn from tempClientConnections
			newConn.isClosed = true
			delete(p.tempConnectionMap, connKey)

			if newConn.connSignalTimer != nil {
				newConn.stopConnSignalTimer()
				newConn.connSignalTimer = nil
			}
			err = fmt.Errorf("dialing PCP connection failed due to timeout")
			log.Println(err)
			return nil, err

		case packet := <-newConn.inputChannel:
			if packet.Flags == SYNFlag|ACKFlag { // Verify if it's a SYN-ACK from the server
				newConn.stopConnSignalTimer() // stops the connection signal resend timer
				newConn.initClientState = SynAckReceived
				//newConn.InitialPeerSeq = packet.SequenceNumber //record the initial SEQ from the peer
				// Prepare ACK packet
				newConn.lastAckNumber = SeqIncrement(packet.SequenceNumber)
				newConn.initialPeerSeq = packet.SequenceNumber
				newConn.initSendAck()

				// Connection established, remove newConn from tempClientConnections, and place it into clientConnections pool
				delete(p.tempConnectionMap, connKey)

				newConn.isOpenConnection = true
				newConn.tcpOptions.timestampEnabled = packet.TcpOptions.timestampEnabled
				// Sack permit and SackOption support
				newConn.tcpOptions.permitSack = newConn.tcpOptions.permitSack && packet.TcpOptions.permitSack    // both sides need to permit SACK
				newConn.tcpOptions.SackEnabled = newConn.tcpOptions.permitSack && newConn.tcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

				p.connectionMap[connKey] = newConn

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
	p.clientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, err = p.clientConn.Read(buffer)
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

	// extract Pcp frame from the received IP frame
	index, err := ExtractIpPayload(buffer[:n])
	if err != nil {
		log.Println("Received IP frame is il-formated. Ignore it!")
		return
	}
	//log.Println("extracted PCP frame length is", len(pcpFrame), pcpFrame)
	// check PCP packet checksum
	// please note the first TcpPseudoHeaderLength bytes are reseved for Tcp pseudo header
	if !VerifyChecksum(buffer[index-TcpPseudoHeaderLength:n], p.serverAddr, p.localAddr, uint8(p.protocolId)) {
		log.Println("Packet checksum verification failed. Skip this packet.")
		return
	}

	// Extract destination port
	pcpFrame := buffer[index:n]
	packet := &PcpPacket{}
	err = packet.Unmarshal(pcpFrame, p.serverAddr, p.localAddr)
	if err != nil {
		log.Println("Received TCP frame is il-formated. Ignore it!")
		// because chunk won't be allocated unless the marshalling is success, there is no need to return the chunk
		return
	}
	//fmt.Println("Received packet Length:", n)
	//fmt.Printf("Got packet:\n %+v\n", packet)

	if rp.Debug && packet.GetChunkReference() != nil {
		fp = packet.AddFootPrint("pcpProtocolConn.handleIncomingPackets")
	}

	// Extract destination IP and port from the packet
	sourcePort := packet.SourcePort
	destPort := packet.DestinationPort

	// Create connection key. Since it's incoming packet
	// source and destination port needs to be reversed when calculating connection key
	connKey := fmt.Sprintf("%d:%d", destPort, sourcePort)

	// First check if the connection exists in the connection map
	conn, ok := p.connectionMap[connKey]
	if ok && !conn.isClosed {
		// open connection. Dispatch the packet to the corresponding connection's input channel
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
			packet.AddChannel("Conn.InputChannel")
		}
		conn.inputChannel <- packet

		return
	}

	// then check if packet belongs to an temp connection.
	tempConn, ok := p.tempConnectionMap[connKey]
	if ok && !tempConn.isClosed {
		if len(packet.Payload) == 0 && packet.AcknowledgmentNum-tempConn.initialSeq < 2 {
			// forward to that connection's input channel
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
				packet.AddChannel("TempConn.InputChannel")
			}
			tempConn.inputChannel <- packet
			return
		} else if len(packet.Payload) > 0 {
			// since the connection is not ready yet, discard the data packet for the time being
			packet.ReturnChunk()
			return
		}
	}

	fmt.Printf("Received data packet for non-existent connection: %s\n", connKey)
	// return the packet chunk now that it's destined to an unknown connection
	packet.ReturnChunk()
}

func (p *PcpProtocolConnection) serverProcessingIncomingPacket(buffer []byte) {
	var fp int

	pcpFrame := buffer[TcpPseudoHeaderLength:] // the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp pseudo header
	// Set a read deadline to ensure non-blocking behavior
	p.serverConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, addr, err := p.serverConn.ReadFrom(pcpFrame)
	if err != nil {
		// Check if the error is a timeout
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// Handle timeout error (no data received within the timeout period)
			return // Continue waiting for incoming packets or handling closeSignal
		}

		log.Println("PcpProtocolConnection.handlingIncomingPackets:Error reading:", err)
		return
	}

	// check PCP packet checksum
	if !VerifyChecksum(buffer[:TcpPseudoHeaderLength+n], addr, p.serverAddr, uint8(p.protocolId)) {
		log.Println("Packet checksum verification failed. Skip this packet.")
		return
	}

	// Extract destination port
	packet := &PcpPacket{}
	err = packet.Unmarshal(pcpFrame[:n], addr, p.serverAddr)
	if err != nil {
		log.Println("Received TCP frame is il-formated. Ignore it!")
		return
	}

	if rp.Debug && packet.GetChunkReference() != nil {
		fp = packet.GetChunkReference().AddFootPrint("pcpProtocolConn.handleIncomingPackets")
	}

	destPort := packet.DestinationPort
	//log.Printf("Got packet with options: %+v\n", packet.TcpOptions)

	// Check if a connection is registered for the packet
	//config.Mu.Lock()
	service, ok := p.serviceMap[int(destPort)]
	//config.Mu.Unlock()

	if !ok {
		//fmt.Println("No service registered for port:", destPort)
		// return the packet's chunk
		packet.ReturnChunk()
		return
	}
	if ok && service.isClosed {
		log.Println("Packet is destined to a closed service. Ignore it.")
		packet.ReturnChunk()
		return
	}

	// Dispatch the packet to the corresponding service's input channel
	if rp.Debug && packet.GetChunkReference() != nil {
		packet.GetChunkReference().TickFootPrint(fp)
		packet.GetChunkReference().AddChannel("Service.InputChannel")
	}
	service.inputChannel <- packet
}

// handleServicePacket is the main service packet dispatches loop.
func (p *PcpProtocolConnection) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	// the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp Pseudo Header
	var buffer []byte
	if p.isServer {
		buffer = make([]byte, p.config.preferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
	} else {
		buffer = make([]byte, p.config.preferredMSS+TcpHeaderLength+TcpOptionsMaxLength+IpHeaderMaxLength+TcpPseudoHeaderLength)
	}

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
		frameBytes = make([]byte, p.config.preferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
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
			packet.GetChunkReference().TickChannel()
			fp = packet.GetChunkReference().AddFootPrint("pcpProtocolConnection.handleOutgoingPackets")
		}

		if p.config.packetLostSimulation {
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
				fmt.Println("Error marshalling packet:", err)
				log.Fatal()
			}
			// Write the packet to the interface
			frame := frameBytes[TcpPseudoHeaderLength:] // first part of framesBytes is actually Tcp Pseudo Header
			if p.isServer {
				_, err = p.serverConn.WriteTo(frame[:n], packet.DestAddr)
			} else {
				_, err = p.clientConn.Write(frame[:n])
			}
			if err != nil {
				log.Println("Error writing packet:", err, "Skip this packet.")
			}
		}

		// add packet to the connection's ResendPackets to wait for acknowledgement from peer or resend if lost
		if len(packet.Payload) > 0 {
			if packet.Conn.tcpOptions.SackEnabled && !packet.IsKeepAliveMassege {
				// if the packet is already in ResendPackets, it is a resend packet. Ignore it. Otherwise, add it to
				if _, found := packet.Conn.resendPackets.GetSentPacket(packet.SequenceNumber); !found {
					packet.Conn.resendPackets.AddSentPacket(packet)
				} else {
					//fmt.Println("this is a resent packet. Do not put it into ResendPackets")
				}
			} else { // SACK is not enabled or it is a keepalive massage
				packet.ReturnChunk() //return its chunk to pool
			}
		}

		if p.config.packetLostSimulation {
			count = (count + 1) % 10
		}
		packetLost = false

		if rp.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().TickFootPrint(fp)
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
			_, ok := p.connectionMap[conn.params.key]
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Client connection does not exist in %s:%d->%s:%d", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.connectionMap, conn.params.key)
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.params.localAddr.(*net.IPAddr).IP.String(), conn.params.localPort, conn.params.remoteAddr.(*net.IPAddr).IP.String(), conn.params.remotePort)

			// if pcpProtocolConnection does not have any connection for 10 seconds, close it
			// Start or reset the timer if ConnectionMap becomes empty
			if len(p.connectionMap) == 0 {
				// Stop the previous timer if it exists
				if p.emptyMapTimer != nil {
					p.emptyMapTimer.Stop()
				}

				// Start a new timer for 10 seconds
				log.Println("Wait for", p.config.pConnTimeout, "seconds before closing the PCP protocol connection")
				p.emptyMapTimer = time.AfterFunc(time.Duration(p.config.pConnTimeout)*time.Second, func() {
					// Close the connection by sending closeSignal to all goroutines and clear resources
					if p.emptyMapTimer != nil {
						p.emptyMapTimer.Stop()
						if len(p.connectionMap) == 0 {
							p.Close()
						}
					}
				})
			}
		case srv := <-p.serviceCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.serviceMap[srv.port] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// Service does not exist in ConnectionMap
				log.Printf("Pcp Service %s:%d does not exist in service map.\n", srv.serviceAddr.(*net.IPAddr).IP.String(), srv.port)
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.serviceMap, srv.port)
			log.Printf("Pcp service %s:%d stopped.", srv.serviceAddr.(*net.IPAddr).IP.String(), srv.port)
		}
	}
}

// Function to close the connection gracefully
func (p *PcpProtocolConnection) Close() {
	// Close all PCP Connections
	for _, conn := range p.connectionMap {
		go conn.Close()
	}
	p.connectionMap = nil // Clear the map after closing all connections

	// Close all connections associated with this service
	for _, srv := range p.serviceMap {
		srv.Close()
	}

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	log.Println("Waiting for go routines to close")
	p.wg.Wait()
	log.Println("Go routines closed")

	// Clear resources
	if !p.isServer {
		p.clientConn.Close() //close protocol connection

		if p.emptyMapTimer != nil {
			p.emptyMapTimer.Stop()
			p.emptyMapTimer = nil
		}

		// Remove iptables rules
		for _, port := range p.iptableRules {
			err := removeIptablesRule(p.serverAddr.IP.To4().String(), port)
			if err != nil {
				log.Printf("Error removing iptables rule for port %d: %v\n", port, err)
			} else {
				log.Printf("Removed iptables rule for port %d\n", port)
			}
		}
		p.iptableRules = nil // Clear the slice
	} // since net.PacketConn is a primitive interface and does not have close method, serverConn does not need to be closed

	close(p.sigOutputChan)
	close(p.outputChan)
	close(p.connCloseSignal)
	close(p.serviceCloseSignal)

	// sent pConn close signal to parent pClient
	p.pConnCloseSignal <- p

	log.Println("PcpProtocolConnection closed gracefully.")
}

func (p *PcpProtocolConnection) getAvailableRandomClientPort() int {
	// Create a map to store all existing client ports
	existingPorts := make(map[int]bool)

	// Populate the map with existing client ports
	for _, conn := range p.connectionMap {
		existingPorts[conn.params.localPort] = true
	}

	for _, conn := range p.tempConnectionMap {
		existingPorts[conn.params.localPort] = true
	}

	// Generate a random port number until it's not in the existingPorts map
	var randomPort int
	for {
		randomPort = rand.Intn(p.config.clientPortUpper-p.config.clientPortLower) + p.config.clientPortLower
		if !existingPorts[randomPort] {
			break
		}
	}

	return randomPort
}

// addIptablesRule adds an iptables rule to drop RST packets originating from the given IP and port.
func addIptablesRule(ip string, port int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// removeIptablesRule removes the iptables rule that was added for dropping RST packets.
func removeIptablesRule(ip string, port int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		// If there is an error executing the command, return the error
		return err
	}

	return nil
}
