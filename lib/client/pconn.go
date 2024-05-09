package client

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
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// pcp client protocol connection struct
type pcpProtocolConnection struct {
	pConnKey                  string
	pcpClientObj              *pcpClient
	ServerAddr, LocalAddr     *net.IPAddr
	pConn                     *net.IPConn
	OutputChan, sigOutputChan chan *lib.PcpPacket
	ConnectionMap             map[string]*lib.Connection
	tempConnectionMap         map[string]*lib.Connection
	ConnCloseSignalChan       chan *lib.Connection
	pConnCloseSignalChan      chan *pcpProtocolConnection
	closeSignal               chan struct{}  // used to send close signal to HandleIncomingPackets, handleOutgoingPackets, handleCloseConnection go routines to stop when timeout arrives
	emptyMapTimer             *time.Timer    // if this pcpProtocolConnection has no connection for 10 seconds, close it
	wg                        sync.WaitGroup // WaitGroup to synchronize goroutines
	iptableRules              []int          // slice of port number on server address which has iptables rules
}

func newPcpProtocolConnection(key string, p *pcpClient, serverAddr, localAddr *net.IPAddr, pConnCloseSignalChan chan *pcpProtocolConnection) (*pcpProtocolConnection, error) {
	// Listen on the PCP protocol (20) at the server IP
	pConn, err := net.DialIP("ip:"+strconv.Itoa(int(p.ProtocolID)), localAddr, serverAddr)
	if err != nil {
		fmt.Println("Error listening:", err)
		return nil, err
	}
	//defer pConn.Close()

	pConnection := &pcpProtocolConnection{
		pConnKey:             key,
		pcpClientObj:         p,
		LocalAddr:            localAddr,
		ServerAddr:           serverAddr,
		pConn:                pConn,
		OutputChan:           make(chan *lib.PcpPacket),
		sigOutputChan:        make(chan *lib.PcpPacket),
		ConnectionMap:        make(map[string]*lib.Connection),
		tempConnectionMap:    make(map[string]*lib.Connection),
		ConnCloseSignalChan:  make(chan *lib.Connection),
		pConnCloseSignalChan: pConnCloseSignalChan,
		closeSignal:          make(chan struct{}),
		wg:                   sync.WaitGroup{},
		iptableRules:         make([]int, 0),
	}

	// Start goroutines
	pConnection.wg.Add(3) // Increase WaitGroup counter by 3 for the three goroutines
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
	newConn, err := lib.NewConnection(connKey, false, p.ServerAddr, int(serverPort), p.LocalAddr, clientPort, p.OutputChan, p.sigOutputChan, p.ConnCloseSignalChan, nil, nil)
	if err != nil {
		fmt.Printf("Error creating new connection to %s:%d because of error: %s\n", p.ServerAddr.IP.To4().String(), serverPort, err)
		return nil, err
	}

	// Add the new connection to the temporary connection map
	p.tempConnectionMap[connKey] = newConn

	// Send SYN to server
	newConn.InitSendSyn()
	newConn.StartConnSignalTimer()
	newConn.NextSequenceNumber = lib.SeqIncrement(newConn.NextSequenceNumber) // implicit modulo op
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
			if packet.Flags == lib.SYNFlag|lib.ACKFlag { // Verify if it's a SYN-ACK from the server
				newConn.StopConnSignalTimer() // stops the connection signal resend timer
				newConn.InitClientState = lib.SynAckReceived
				//newConn.InitialPeerSeq = packet.SequenceNumber //record the initial SEQ from the peer
				// Prepare ACK packet
				newConn.LastAckNumber = lib.SeqIncrement(packet.SequenceNumber)
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
				lib.SleepForMs(config.AppConfig.IptableRuleDaley)

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

// handleServicePacket is the main service packet dispatches loop.
func (p *pcpProtocolConnection) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	var (
		err error
		n   int
	)
	// the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp Pseudo Header
	buffer := make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.IpHeaderMaxLength)
	// main loop for incoming packets
	for {
		select {
		case <-p.closeSignal:
			return
		default:
			// Set a read deadline to ensure non-blocking behavior
			p.pConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // Example timeout of 100 milliseconds

			n, err = p.pConn.Read(buffer)
			if err != nil {
				// Check if the error is a timeout
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Handle timeout error (no data received within the timeout period)
					continue // Continue waiting for incoming packets or handling closeSignal
				}

				// other errors
				log.Println("pcpProtocolConnection.handleIncomingPackets:Error reading:", err)
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
				// because chunk won't be allocated unless the marshalling is success, there is no need to return the chunk
				continue
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
				continue
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
					continue
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
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *pcpProtocolConnection) handleOutgoingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	var (
		count      = 0
		lostCount  = 0
		frameBytes = make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.TcpPseudoHeaderLength)
		n          = 0
		err        error
		packet     *lib.PcpPacket
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
			case packet = <-p.OutputChan:
			}
		}

		// Subscribe to p.OutputChan
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
func (p *pcpProtocolConnection) handleCloseConnection() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return
		case conn := <-p.ConnCloseSignalChan:
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
		}
	}
}

// Function to close the connection gracefully
func (p *pcpProtocolConnection) Close() {
	// Close all PCP Connections
	for _, conn := range p.ConnectionMap {
		go conn.Close()
	}
	p.ConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	log.Println("Waiting for go routines to close")
	p.wg.Wait()
	log.Println("Go routines closed")

	// Clear resources
	p.pConn.Close() //close protocol connection

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

	close(p.sigOutputChan)
	close(p.ConnCloseSignalChan)
	close(p.OutputChan)

	// sent pConn close signal to parent pClient
	p.pConnCloseSignalChan <- p

	log.Println("PcpProtocolConnection closed gracefully.")
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
