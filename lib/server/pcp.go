package server

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os/exec"
	"strconv"
	"sync"

	//"time"
	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type PcpServer struct {
	ProtocolID         uint8
	ProtoConnectionMap map[string]*PcpProtocolConnection // keep track of all protocolConn created by dialIP
	pConnCloseSignal   chan *PcpProtocolConnection
	closeSignal        chan struct{}  // used to send close signal to go routines to stop when timeout arrives
	wg                 sync.WaitGroup // WaitGroup to synchronize goroutines
}

func NewPcpServer(protocolId uint8) (*PcpServer, error) {
	// starts the PCP protocol client main service
	// the main role is to create pcpServer object - one per system

	// create map for pcpServerProtocolConnection
	protoConnectionMap := make(map[string]*PcpProtocolConnection)

	pcpServerObj := &PcpServer{
		ProtocolID:         protocolId,
		ProtoConnectionMap: protoConnectionMap,
		pConnCloseSignal:   make(chan *PcpProtocolConnection),
		closeSignal:        make(chan struct{}),
	}

	lib.Pool = lib.NewPayloadPool(config.AppConfig.PayloadPoolSize, config.AppConfig.PreferredMSS)

	// Start goroutines
	pcpServerObj.wg.Add(1) // Increase WaitGroup counter by 1 for the handleClosePConnConnection goroutines
	go pcpServerObj.handleClosePConnConnection()

	fmt.Println("Pcp protocol client started")

	return pcpServerObj, nil
}

func (p *PcpServer) handleClosePConnConnection() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return // gracefully stop the go routine
		case pConn := <-p.pConnCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.ProtoConnectionMap[pConn.pConnKey] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Protocol Connection %s does not exist in service map", pConn.ServerAddr.(*net.IPAddr).IP.String())
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.ProtoConnectionMap, pConn.pConnKey)
			log.Printf("Pcp protocol connection %s terminated and removed.", pConn.ServerAddr.(*net.IPAddr).IP.String())
		}
	}
}

func (p *PcpServer) Close() error {
	// Close all pcpProtocolConnection instances
	for _, pConn := range p.ProtoConnectionMap {
		pConn.Close()
	}
	p.ProtoConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	p.wg.Wait()

	close(p.pConnCloseSignal)

	log.Println("PcpServer closed gracefully.")

	return nil
}

// pcp protocol server struct
type PcpProtocolConnection struct {
	pConnKey                  string
	pcpServerObj              *PcpServer
	ServerAddr                net.Addr
	Connection                net.PacketConn
	OutputChan, sigOutputChan chan *lib.PcpPacket
	ServiceMap                map[int]*Service
	serviceCloseSignal        chan *Service
	pConnCloseSignal          chan *PcpProtocolConnection
	closeSignal               chan struct{}  // used to send close signal to go routines to stop when timeout arrives
	wg                        sync.WaitGroup // WaitGroup to synchronize goroutines
}

func newPcpServerProtocolConnection(key string, p *PcpServer, serverIP string, pConnCloseSignal chan *PcpProtocolConnection) (*PcpProtocolConnection, error) { // serverIP must be a specific IP, not 0.0.0.0
	serverAddr, err := net.ResolveIPAddr("ip", serverIP)
	if err != nil {
		fmt.Println("Error:", err)
		return nil, err
	}
	// Listen on the PCP protocol (20) at the server IP
	protocolConn, err := net.ListenPacket("ip:"+strconv.Itoa(int(p.ProtocolID)), serverIP)
	if err != nil {
		fmt.Println("Error listening:", err)
		return nil, err
	}
	//defer protocolConn.Close()

	fmt.Println("Pcp protocol Server started")

	pcpObj := &PcpProtocolConnection{
		pConnKey:           key,
		pcpServerObj:       p,
		ServerAddr:         serverAddr,
		Connection:         protocolConn,
		OutputChan:         make(chan *lib.PcpPacket),
		sigOutputChan:      make(chan *lib.PcpPacket),
		ServiceMap:         make(map[int]*Service),
		serviceCloseSignal: make(chan *Service),
		pConnCloseSignal:   pConnCloseSignal,
		closeSignal:        make(chan struct{}),
		wg:                 sync.WaitGroup{},
	}

	// Start goroutines to handle incoming and outgoing packets
	// Start goroutines
	pcpObj.wg.Add(3) // Increase WaitGroup counter by 3 for the three goroutines
	go pcpObj.handlingIncomingPackets()
	go pcpObj.handleOutgoingPackets()
	go pcpObj.handleCloseService()

	return pcpObj, nil
}

func (p *PcpProtocolConnection) handlingIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	// Continuously read from the protocolConn
	buffer := make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.TcpPseudoHeaderLength)
	pcpFrame := buffer[lib.TcpPseudoHeaderLength:] // the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp pseudo header
	for {
		n, addr, err := p.Connection.ReadFrom(pcpFrame)
		if err != nil {
			log.Println("PcpProtocolConnection.handlingIncomingPackets:Error reading:", err)
			continue
		}

		// check PCP packet checksum
		if !lib.VerifyChecksum(buffer[:lib.TcpPseudoHeaderLength+n], addr, p.ServerAddr, p.pcpServerObj.ProtocolID) {
			log.Println("Packet checksum verification failed. Skip this packet.")
			continue
		}

		// Extract destination port
		packet := &lib.PcpPacket{}
		err = packet.Unmarshal(pcpFrame[:n], addr, p.ServerAddr)
		if err != nil {
			log.Println("Received TCP frame is il-formated. Ignore it!")
			continue
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
			continue
		}
		if ok && service.IsClosed {
			log.Println("Packet is destined to a closed service. Ignore it.")
			packet.ReturnChunk()
			continue
		}

		// Dispatch the packet to the corresponding service's input channel
		if config.Debug && packet.GetChunkReference() != nil {
			packet.GetChunkReference().AddToChannel("Service.InputChannel")
			packet.GetChunkReference().PopCallStack()
		}
		service.InputChannel <- packet
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *PcpProtocolConnection) handleOutgoingPackets() {
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
			return // gracefully close the go routine
		case packet = <-p.sigOutputChan: // Subscribe to the priority p.sigOutputChan
		default:
			select {
			case <-p.closeSignal:
				return // gracefully close the go routine
			case packet = <-p.sigOutputChan: // Subscribe to the priority p.sigOutputChan
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
				lostCount = 100
				log.Println("Packet", count, "is lost")
				packetLost = true
			}
		}

		if !packetLost {
			// Marshal the packet into bytes
			n, err = packet.Marshal(p.pcpServerObj.ProtocolID, frameBytes)
			if err != nil {
				fmt.Println("Error marshalling packet:", err)
				log.Fatal()
			}
			// Write the packet to the interface
			frame := frameBytes[lib.TcpPseudoHeaderLength:] // first part of framesBytes is actually Tcp Pseudo Header
			_, err = p.Connection.WriteTo(frame[:n], packet.DestAddr)
			if err != nil {
				fmt.Println("Error writing packet:", err, "Skip this packet.")
			}
		}

		// add packet to the connection's ResendPackets to wait for acknowledgement from peer
		if len(packet.Payload) > 0 {
			if packet.Conn.TcpOptions.SackEnabled && !packet.IsKeepAliveMassege {
				// if the packet is already in RevPacketCache, it is a resend packet. Ignore it. Otherwise, add it to
				if _, found := packet.Conn.ResendPackets.GetSentPacket(packet.SequenceNumber); !found {
					packet.Conn.ResendPackets.AddSentPacket(packet)
				}
			} else { // SACK is not enabled or it is a keepalive message
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

func (p *PcpProtocolConnection) handleCloseService() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return // gracefully close the go routine
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

func (p *PcpProtocolConnection) Close() error {
	// Close all connections associated with this service
	for _, srv := range p.ServiceMap {
		srv.Close()
	}

	// send signal to service go routines to gracefully close them
	close(p.closeSignal)

	p.wg.Wait()

	// close channels created by the service
	close(p.OutputChan)
	close(p.sigOutputChan)
	close(p.serviceCloseSignal)

	// send signal to parent pcpServer to clear the PcpProtocolConnection's resource
	p.pConnCloseSignal <- p

	return nil
}

// ListenPcp starts listening for incoming packets on the service's port.
func (p *PcpServer) ListenPcp(serviceIP string, port int) (*Service, error) {
	// first check if corresponding PcpServerProtocolConnection obj exists or not
	// Normalize IP address string before making key from it
	serviceAddr, err := net.ResolveIPAddr("ip", serviceIP)
	if err != nil {
		log.Println("IP address is malformated:", err)
		return nil, err
	}
	normServiceIpString := serviceAddr.IP.To4().String()

	pConnKey := normServiceIpString
	// Check if the connection exists in the connection map
	pConn, ok := p.ProtoConnectionMap[pConnKey]
	if !ok {
		// need to create new protocol connection
		pConn, err = newPcpServerProtocolConnection(pConnKey, p, normServiceIpString, p.pConnCloseSignal)
		if err != nil {
			log.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.ProtoConnectionMap[pConnKey] = pConn
	}

	// then we need to check if there is already a service listening at that serviceIP and port
	_, ok = pConn.ServiceMap[port]
	if !ok {
		// need to create new service
		// create new Pcp service
		srv, err := newService(pConn, serviceAddr, port, pConn.OutputChan, pConn.sigOutputChan, pConn.serviceCloseSignal)
		if err != nil {
			log.Println("Error creating service:", err)
			return nil, err
		}

		// Add iptables rule to drop RST packets created by system TCP/IP network stack
		if err := addIptablesRule(serviceIP, port); err != nil {
			log.Println("Error adding iptables rule:", err)
			return nil, err
		}

		// add it to ServiceMap
		pConn.ServiceMap[port] = srv

		return srv, nil
	} else {
		err = fmt.Errorf("%s:%d is already taken", serviceIP, port)
		return nil, err
	}
}

// addIptablesRule adds an iptables rule to drop RST packets originating from the given IP and port.
func addIptablesRule(ip string, port int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// removeIptablesRule removes the iptables rule that was added for dropping RST packets.
func removeIptablesRule(ip string, port int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		// If there is an error executing the command, return the error
		return err
	}

	return nil
}
