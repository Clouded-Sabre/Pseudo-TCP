package client

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os/exec"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// pcp client protocol connection struct
type pcpProtocolConnection struct {
	pcpClientObj          *pcpClient
	ServerAddr, LocalAddr *net.IPAddr
	pConn                 *net.IPConn
	OutputChan            chan *lib.PcpPacket
	ConnectionMap         map[string]*lib.Connection
	tempConnectionMap     map[string]*lib.Connection
	ConnCloseSignalChan   chan *lib.Connection
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
		pcpClientObj:        p,
		LocalAddr:           localAddr,
		ServerAddr:          serverAddr,
		pConn:               pConn,
		OutputChan:          make(chan *lib.PcpPacket),
		ConnectionMap:       make(map[string]*lib.Connection),
		tempConnectionMap:   make(map[string]*lib.Connection),
		ConnCloseSignalChan: make(chan *lib.Connection),
	}

	go pConnection.handleIncomingPackets()

	go pConnection.handleOutgoingPackets()

	go pConnection.handleCloseConnection()

	return pConnection, nil
}

func (p *pcpProtocolConnection) dial(serverPort int) (*lib.Connection, error) {
	// Choose a random client port
	clientPort := p.getAvailableRandomClientPort()

	// Create connection key
	connKey := fmt.Sprintf("%d:%d", clientPort, serverPort)

	// Create a new temporary connection object for the 3-way handshake
	newConn, err := lib.NewConnection(connKey, p.ServerAddr, int(serverPort), p.LocalAddr, clientPort, p.OutputChan, p.ConnCloseSignalChan, nil)
	if err != nil {
		fmt.Printf("Error creating new connection to %s:%d because of error: %s\n", p.ServerAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.tempConnectionMap[connKey] = newConn

	// Send SYN to server
	newConn.InitSendSyn()
	newConn.StartConnSignalTimer()
	newConn.NextSequenceNumber = uint32(uint64(newConn.NextSequenceNumber) + 1) // implicit modulo op
	log.Println("Initiated connection to server with connKey:", connKey)

	// Wait for SYN-ACK
	// Create a loop to read from connection's input channel till we see SYN-ACK packet from the other end
	for {
		packet := <-newConn.InputChannel
		if packet.Flags == lib.SYNFlag|lib.ACKFlag { // Verify if it's a SYN-ACK from the server
			newConn.StopConnSignalTimer() // stops the connection signal resend timer
			newConn.InitClientState = lib.SynAckReceived
			newConn.InitialPeerSeq = packet.SequenceNumber //record the initial SEQ from the peer
			// Prepare ACK packet
			newConn.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1)
			newConn.InitSendAck()

			// Connection established, remove newConn from tempClientConnections, and place it into clientConnections pool
			delete(p.tempConnectionMap, connKey)

			// Add iptables rule to drop RST packets
			if err := addIptablesRule(p.ServerAddr.IP.To4().String(), serverPort); err != nil {
				log.Println("Error adding iptables rule:", err)
				return nil, err
			}

			newConn.IsOpenConnection = true
			newConn.TcpOptions.TimestampEnabled = packet.TcpOptions.TimestampEnabled
			// Sack permit and SackOption support
			newConn.TcpOptions.PermitSack = newConn.TcpOptions.PermitSack && packet.TcpOptions.PermitSack    // both sides need to permit SACK
			newConn.TcpOptions.SackEnabled = newConn.TcpOptions.PermitSack && newConn.TcpOptions.SackEnabled // Sack Option support also needs to be manually enabled

			p.ConnectionMap[connKey] = newConn

			// start go routine to handle incoming packets for new connection
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

// addIptablesRule adds an iptables rule to drop RST packets originating from the given IP and port.
func addIptablesRule(ip string, port int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// handleServicePacket is the main service packet dispatches loop.
func (p *pcpProtocolConnection) handleIncomingPackets() {
	var (
		err error
		n   int
	)
	// the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp Pseudo Header
	buffer := make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.IpHeaderMaxLength)
	// main loop for incoming packets
	for {
		n, err = p.pConn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}

		// extract Pcp frame from the received IP frame
		index, err := lib.ExtractIpPayload(buffer[:n])
		if err != nil {
			log.Println("Received IP frame is il-formated. Ignore it!")
			continue
		}
		//log.Println("extracted PCP frame length is", len(pcpFrame), pcpFrame)
		// check PCP packet checksum
		// please note the first lib.TcpPseudoHeaderLength bytes are reseved for Tcp pseudo header
		if !lib.VerifyChecksum(buffer[index-lib.TcpPseudoHeaderLength:n], p.ServerAddr, p.LocalAddr, uint8(p.pcpClientObj.ProtocolID)) {
			log.Println("Packet checksum verification failed. Skip this packet.")
			continue
		}

		// Extract destination port
		pcpFrame := buffer[index:n]
		packet := &lib.PcpPacket{}
		err = packet.Unmarshal(pcpFrame, p.ServerAddr, p.LocalAddr)
		if err != nil {
			log.Println("Received TCP frame is il-formated. Ignore it!")
			continue
		}
		//fmt.Println("Received packet Length:", n)
		//fmt.Printf("Got packet:\n %+v\n", packet)

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
	var (
		count      = 0
		lostCount  = 0
		frameBytes = make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.TcpPseudoHeaderLength)
		n          = 0
		err        error
	)
	packetLost := false
	for {
		packet := <-p.OutputChan // Subscribe to p.OutputChan
		/*if len(packet.Data.Payload) > 0 {
			fmt.Println("outgoing packet payload is", packet.Data.Payload)
		}*/
		//fmt.Println("PTC got packet.")

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
			n, err = packet.Marshal(p.pcpClientObj.ProtocolID, frameBytes)
			if err != nil {
				fmt.Println("Error marshalling packet:", err)
				log.Fatal()
			}
			// Write the packet to the interface
			_, err = p.pConn.Write(frameBytes[lib.TcpPseudoHeaderLength : lib.TcpPseudoHeaderLength+n]) // first part of framesBytes is actually Tcp Pseudo Header
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

		if packet.IsOpenConnection && config.AppConfig.PacketLostSimulation {
			count = (count + 1) % 10
		}
		packetLost = false
	}
}

// handle close connection request from ClientConnection
func (p *pcpProtocolConnection) handleCloseConnection() {
	for {
		conn := <-p.ConnCloseSignalChan
		// clear it from p.ConnectionMap
		_, ok := p.ConnectionMap[conn.Key]
		if !ok {
			// connection does not exist in ConnectionMap
			log.Printf("Pcp Client connection does not exist in %s:%d->%s:%d", conn.LocalAddr.(*net.IPAddr).IP.String(), conn.LocalPort, conn.RemoteAddr.(*net.IPAddr).IP.String(), conn.RemotePort)
			continue
		}

		// delete the clientConn from ConnectionMap
		delete(p.ConnectionMap, conn.Key)
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
		randomPort = rand.Intn(config.AppConfig.ClientPortUpper-config.AppConfig.ClientPortLower) + config.AppConfig.ClientPortLower
		if !existingPorts[randomPort] {
			break
		}
	}

	return randomPort
}
