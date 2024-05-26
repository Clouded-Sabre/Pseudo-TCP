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
)

// pcp protocol connection struct
type PcpProtocolConnection struct {
	Key                       string                      // PCP protocol connection's key
	protocolId                int                         // protocol id
	isServer                  bool                        // Role of the object
	ServerAddr, LocalAddr     *net.IPAddr                 // server and local ip address
	ClientConn                *net.IPConn                 // used for client side only
	ServerConn                net.PacketConn              // used for server side only
	OutputChan, sigOutputChan chan *PcpPacket             // output channel for normal packets and signalling packets respectively
	ConnectionMap             map[string]*Connection      // used for client side only
	tempConnectionMap         map[string]*Connection      // used for client side only
	ConnCloseSignal           chan *Connection            // receiving close signal from pcp connection. used for client side only
	ServiceMap                map[int]*Service            // used for server side only
	serviceCloseSignal        chan *Service               // receiving close signal from pcp service. used for service side only
	pConnCloseSignal          chan *PcpProtocolConnection // used to send signal to parent service to tell it to clear this connection from its map
	closeSignal               chan struct{}               // used to send close signal to HandleIncomingPackets, handleOutgoingPackets, handleCloseConnection go routines to stop when timeout arrives
	emptyMapTimer             *time.Timer                 // if this pcpProtocolConnection has no connection for 10 seconds, close it. used for client side only
	wg                        sync.WaitGroup              // WaitGroup to synchronize goroutines
	iptableRules              []int                       // slice of port number on server address which has iptables rules. used for client side only
}

func newPcpProtocolConnection(key string, isServer bool, protocolId int, serverAddr, localAddr *net.IPAddr, pConnCloseSignal chan *PcpProtocolConnection) (*PcpProtocolConnection, error) {
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
		Key:                key,
		isServer:           isServer,
		protocolId:         protocolId,
		LocalAddr:          localAddr,
		ServerAddr:         serverAddr,
		ClientConn:         clientConn,
		ServerConn:         serverConn,
		OutputChan:         make(chan *PcpPacket),
		sigOutputChan:      make(chan *PcpPacket),
		ConnectionMap:      make(map[string]*Connection),
		tempConnectionMap:  make(map[string]*Connection),
		ConnCloseSignal:    make(chan *Connection),
		serviceCloseSignal: make(chan *Service),
		ServiceMap:         make(map[int]*Service),

		pConnCloseSignal: pConnCloseSignal,
		closeSignal:      make(chan struct{}),
		wg:               sync.WaitGroup{},
		iptableRules:     make([]int, 0),
	}

	// Start goroutines
	pConnection.wg.Add(3) // Increase WaitGroup counter by 3 for the three goroutines
	go pConnection.handleIncomingPackets()
	go pConnection.handleOutgoingPackets()
	go pConnection.handleCloseConnection()

	return pConnection, nil
}

func (p *PcpProtocolConnection) dial(serverPort int, connConfig *ConnectionConfig) (*Connection, error) {
	// Choose a random client port
	clientPort := p.getAvailableRandomClientPort()

	// Create connection key
	connKey := fmt.Sprintf("%d:%d", clientPort, serverPort)

	connParam := &ConnectionParams{
		Key:                      connKey,
		IsServer:                 false,
		RemoteAddr:               p.ServerAddr,
		RemotePort:               int(serverPort),
		LocalAddr:                p.LocalAddr,
		LocalPort:                clientPort,
		OutputChan:               p.OutputChan,
		SigOutputChan:            p.sigOutputChan,
		ConnCloseSignalChan:      p.ConnCloseSignal,
		NewConnChannel:           nil,
		ConnSignalFailedToParent: nil,

		WindowScale:             connConfig.WindowScale,
		PreferredMSS:            connConfig.PreferredMSS,
		SackPermitSupport:       connConfig.SackPermitSupport,
		SackOptionSupport:       connConfig.SackOptionSupport,
		IdleTimeout:             connConfig.IdleTimeout,
		KeepAliveEnabled:        connConfig.KeepAliveEnabled,
		KeepaliveInterval:       connConfig.KeepaliveInterval,
		MaxKeepaliveAttempts:    connConfig.MaxKeepaliveAttempts,
		ResendInterval:          connConfig.ResendInterval,
		MaxResendCount:          connConfig.MaxResendCount,
		Debug:                   connConfig.Debug,
		WindowSizeWithScale:     connConfig.WindowSizeWithScale,
		ConnSignalRetryInterval: connConfig.ConnSignalRetryInterval,
		ConnSignalRetry:         connConfig.ConnSignalRetry,
	}

	// Create a new temporary connection object for the 3-way handshake
	//newConn, err := NewConnection(connKey, false, p.ServerAddr, int(serverPort), p.LocalAddr, clientPort, p.OutputChan, p.sigOutputChan, p.ConnCloseSignal, nil, nil)
	newConn, err := NewConnection(connParam)
	if err != nil {
		fmt.Printf("Error creating new connection to %s:%d because of error: %s\n", p.ServerAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.tempConnectionMap[connKey] = newConn

	// Send SYN to server
	newConn.InitSendSyn()
	newConn.StartConnSignalTimer()
	newConn.NextSequenceNumber = SeqIncrement(newConn.NextSequenceNumber) // implicit modulo op
	log.Println("Initiated connection to server with connKey:", connKey)

	// Wait for SYN-ACK
	// Create a loop to read from connection's input channel till we see SYN-ACK packet from the other end
	for {
		select {
		case <-newConn.ConnSignalFailed:
			// dial action failed
			// Connection dialing failed, remove newConn from tempClientConnections
			newConn.IsClosed = true
			delete(p.tempConnectionMap, connKey)

			if newConn.ConnSignalTimer != nil {
				newConn.StopConnSignalTimer()
				newConn.ConnSignalTimer = nil
			}
			err = fmt.Errorf("dialing PCP connection failed due to timeout")
			log.Println(err)
			return nil, err

		case packet := <-newConn.InputChannel:
			if packet.Flags == SYNFlag|ACKFlag { // Verify if it's a SYN-ACK from the server
				newConn.StopConnSignalTimer() // stops the connection signal resend timer
				newConn.InitClientState = SynAckReceived
				//newConn.InitialPeerSeq = packet.SequenceNumber //record the initial SEQ from the peer
				// Prepare ACK packet
				newConn.LastAckNumber = SeqIncrement(packet.SequenceNumber)
				newConn.InitialPeerSeq = packet.SequenceNumber
				newConn.InitSendAck()

				// Connection established, remove newConn from tempClientConnections, and place it into clientConnections pool
				delete(p.tempConnectionMap, connKey)

				// Add iptables rule to drop RST packets
				if err := addIptablesRule(p.ServerAddr.IP.To4().String(), serverPort); err != nil {
					log.Println("Error adding iptables rule:", err)
					return nil, err
				}
				p.iptableRules = append(p.iptableRules, serverPort) // record it for later deletion of the rules when connection closes

				// sleep for 200ms to make sure iptable rule takes effect
				SleepForMs(config.AppConfig.IptableRuleDaley)

				newConn.IsOpenConnection = true
				newConn.TcpOptions.TimestampEnabled = packet.TcpOptions.TimestampEnabled
				// Sack permit and SackOption support
				newConn.TcpOptions.PermitSack = newConn.TcpOptions.PermitSack && packet.TcpOptions.PermitSack    // both sides need to permit SACK
				newConn.TcpOptions.SackEnabled = newConn.TcpOptions.PermitSack && newConn.TcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

				p.ConnectionMap[connKey] = newConn

				// start go routine to handle incoming packets for new connection
				newConn.Wg.Add(1)
				go newConn.HandleIncomingPackets()
				// start ResendTimer if Sack Enabled
				if newConn.TcpOptions.SackEnabled {
					// Start the resend timer
					newConn.StartResendTimer()
				}

				newConn.InitClientState = 0 // reset it to zero so that later ACK message can be processed correctly

				return newConn, nil
			}
		}

	}
}

func (p *PcpProtocolConnection) clientProcessingIncomingPacket(buffer []byte) {
	var (
		err error
		n   int
	)
	// Set a read deadline to ensure non-blocking behavior
	p.ClientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, err = p.ClientConn.Read(buffer)
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
	if !VerifyChecksum(buffer[index-TcpPseudoHeaderLength:n], p.ServerAddr, p.LocalAddr, uint8(p.protocolId)) {
		log.Println("Packet checksum verification failed. Skip this packet.")
		return
	}

	// Extract destination port
	pcpFrame := buffer[index:n]
	packet := &PcpPacket{}
	err = packet.Unmarshal(pcpFrame, p.ServerAddr, p.LocalAddr)
	if err != nil {
		log.Println("Received TCP frame is il-formated. Ignore it!")
		// because chunk won't be allocated unless the marshalling is success, there is no need to return the chunk
		return
	}
	//fmt.Println("Received packet Length:", n)
	//fmt.Printf("Got packet:\n %+v\n", packet)

	if config.Debug && packet.GetChunkReference() != nil {
		packet.GetChunkReference().AddCallStack("pcpProtocolConn.handleIncomingPackets")
	}

	// Extract destination IP and port from the packet
	sourcePort := packet.SourcePort
	destPort := packet.DestinationPort

	// Create connection key. Since it's incoming packet
	// source and destination port needs to be reversed when calculating connection key
	connKey := fmt.Sprintf("%d:%d", destPort, sourcePort)

	// First check if the connection exists in the connection map
	conn, ok := p.ConnectionMap[connKey]
	if ok && !conn.IsClosed {
		// open connection. Dispatch the packet to the corresponding connection's input channel
		conn.InputChannel <- packet
		if config.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().AddToChannel("Conn.InputChannel")
			packet.GetChunkReference().PopCallStack()
		}
		return
	}

	// then check if packet belongs to an temp connection.
	tempConn, ok := p.tempConnectionMap[connKey]
	if ok && !tempConn.IsClosed {
		if len(packet.Payload) == 0 && packet.AcknowledgmentNum-tempConn.InitialSeq < 2 {
			// forward to that connection's input channel
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

	fmt.Printf("Received data packet for non-existent connection: %s\n", connKey)
	// return the packet chunk now that it's destined to an unknown connection
	packet.ReturnChunk()
}

func (p *PcpProtocolConnection) serverProcessingIncomingPacket(buffer []byte) {
	pcpFrame := buffer[TcpPseudoHeaderLength:] // the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp pseudo header
	// Set a read deadline to ensure non-blocking behavior
	p.ServerConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

	n, addr, err := p.ServerConn.ReadFrom(pcpFrame)
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
	if !VerifyChecksum(buffer[:TcpPseudoHeaderLength+n], addr, p.ServerAddr, uint8(p.protocolId)) {
		log.Println("Packet checksum verification failed. Skip this packet.")
		return
	}

	// Extract destination port
	packet := &PcpPacket{}
	err = packet.Unmarshal(pcpFrame[:n], addr, p.ServerAddr)
	if err != nil {
		log.Println("Received TCP frame is il-formated. Ignore it!")
		return
	}

	if config.Debug && packet.GetChunkReference() != nil {
		packet.GetChunkReference().AddCallStack("pcpProtocolConn.handleIncomingPackets")
	}

	destPort := packet.DestinationPort
	//log.Printf("Got packet with options: %+v\n", packet.TcpOptions)

	// Check if a connection is registered for the packet
	config.Mu.Lock()
	service, ok := p.ServiceMap[int(destPort)]
	config.Mu.Unlock()

	if !ok {
		//fmt.Println("No service registered for port:", destPort)
		// return the packet's chunk
		packet.ReturnChunk()
		return
	}
	if ok && service.IsClosed {
		log.Println("Packet is destined to a closed service. Ignore it.")
		packet.ReturnChunk()
		return
	}

	// Dispatch the packet to the corresponding service's input channel
	if config.Debug && packet.GetChunkReference() != nil {
		packet.GetChunkReference().AddToChannel("Service.InputChannel")
		packet.GetChunkReference().PopCallStack()
	}
	service.InputChannel <- packet
}

// handleServicePacket is the main service packet dispatches loop.
func (p *PcpProtocolConnection) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	// the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp Pseudo Header
	var buffer []byte
	if p.isServer {
		buffer = make([]byte, config.AppConfig.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
	} else {
		buffer = make([]byte, config.AppConfig.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+IpHeaderMaxLength+TcpPseudoHeaderLength)
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
		frameBytes = make([]byte, config.AppConfig.PreferredMSS+TcpHeaderLength+TcpOptionsMaxLength+TcpPseudoHeaderLength)
		n          = 0
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
			case packet = <-p.OutputChan: // Subscribe to p.OutputChan
			}
		}

		if config.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().RemoveFromChannel()
			packet.GetChunkReference().AddCallStack("pcpProtocolConnection.handleOutgoingPackets")
		}

		if config.AppConfig.PacketLostSimulation {
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
				_, err = p.ServerConn.WriteTo(frame[:n], packet.DestAddr)
			} else {
				_, err = p.ClientConn.Write(frame[:n])
			}
			if err != nil {
				log.Println("Error writing packet:", err, "Skip this packet.")
			}
		}

		// add packet to the connection's ResendPackets to wait for acknowledgement from peer or resend if lost
		if len(packet.Payload) > 0 {
			if packet.Conn.TcpOptions.SackEnabled && !packet.IsKeepAliveMassege {
				// if the packet is already in ResendPackets, it is a resend packet. Ignore it. Otherwise, add it to
				if _, found := packet.Conn.ResendPackets.GetSentPacket(packet.SequenceNumber); !found {
					packet.Conn.ResendPackets.AddSentPacket(packet)
				} else {
					//fmt.Println("this is a resent packet. Do not put it into ResendPackets")
				}
			} else { // SACK is not enabled or it is a keepalive massage
				packet.ReturnChunk() //return its chunk to pool
			}
		}

		if config.AppConfig.PacketLostSimulation {
			count = (count + 1) % 10
		}
		packetLost = false

		if config.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().PopCallStack()
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
		case conn := <-p.ConnCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.ConnectionMap[conn.config.Key]
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Client connection does not exist in %s:%d->%s:%d", conn.config.LocalAddr.(*net.IPAddr).IP.String(), conn.config.LocalPort, conn.config.RemoteAddr.(*net.IPAddr).IP.String(), conn.config.RemotePort)
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.ConnectionMap, conn.config.Key)
			log.Printf("Pcp connection %s:%d->%s:%d terminated and removed.", conn.config.LocalAddr.(*net.IPAddr).IP.String(), conn.config.LocalPort, conn.config.RemoteAddr.(*net.IPAddr).IP.String(), conn.config.RemotePort)

			// if pcpProtocolConnection does not have any connection for 10 seconds, close it
			// Start or reset the timer if ConnectionMap becomes empty
			if len(p.ConnectionMap) == 0 {
				// Stop the previous timer if it exists
				if p.emptyMapTimer != nil {
					p.emptyMapTimer.Stop()
				}

				// Start a new timer for 10 seconds
				log.Println("Wait for", config.AppConfig.PConnTimeout, "seconds before closing the PCP protocol connection")
				p.emptyMapTimer = time.AfterFunc(time.Duration(config.AppConfig.PConnTimeout)*time.Second, func() {
					// Close the connection by sending closeSignal to all goroutines and clear resources
					if p.emptyMapTimer != nil {
						p.emptyMapTimer.Stop()
						if len(p.ConnectionMap) == 0 {
							p.Close()
						}
					}
				})
			}
		case srv := <-p.serviceCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.ServiceMap[srv.Port] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// Service does not exist in ConnectionMap
				log.Printf("Pcp Service %s:%d does not exist in service map.\n", srv.ServiceAddr.(*net.IPAddr).IP.String(), srv.Port)
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.ServiceMap, srv.Port)
			log.Printf("Pcp service %s:%d stopped.", srv.ServiceAddr.(*net.IPAddr).IP.String(), srv.Port)
		}
	}
}

// Function to close the connection gracefully
func (p *PcpProtocolConnection) Close() {
	// Close all PCP Connections
	for _, conn := range p.ConnectionMap {
		go conn.Close()
	}
	p.ConnectionMap = nil // Clear the map after closing all connections

	// Close all connections associated with this service
	for _, srv := range p.ServiceMap {
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
		p.ClientConn.Close() //close protocol connection

		if p.emptyMapTimer != nil {
			p.emptyMapTimer.Stop()
			p.emptyMapTimer = nil
		}

		// Remove iptables rules
		for _, port := range p.iptableRules {
			err := removeIptablesRule(p.ServerAddr.IP.To4().String(), port)
			if err != nil {
				log.Printf("Error removing iptables rule for port %d: %v\n", port, err)
			} else {
				log.Printf("Removed iptables rule for port %d\n", port)
			}
		}
		p.iptableRules = nil // Clear the slice
	} // since net.PacketConn is a primitive interface and does not have close method, serverConn does not need to be closed

	close(p.sigOutputChan)
	close(p.OutputChan)
	close(p.ConnCloseSignal)
	close(p.serviceCloseSignal)

	// sent pConn close signal to parent pClient
	p.pConnCloseSignal <- p

	log.Println("PcpProtocolConnection closed gracefully.")
}

func (p *PcpProtocolConnection) getAvailableRandomClientPort() int {
	// Create a map to store all existing client ports
	existingPorts := make(map[int]bool)

	// Populate the map with existing client ports
	for _, conn := range p.ConnectionMap {
		existingPorts[conn.config.LocalPort] = true
	}

	for _, conn := range p.tempConnectionMap {
		existingPorts[conn.config.LocalPort] = true
	}

	// Generate a random port number until it's not in the existingPorts map
	var randomPort int
	for {
		randomPort = rand.Intn(config.AppConfig.ClientPortUpper-config.AppConfig.ClientPortLower) + config.AppConfig.ClientPortLower
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
