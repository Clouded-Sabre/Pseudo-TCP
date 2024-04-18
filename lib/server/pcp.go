package server

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"os/exec"
	"strconv"

	//"time"
	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type PcpServer struct {
	ProtocolID         uint8
	ProtoConnectionMap map[string]*PcpProtocolConnection // keep track of all protocolConn created by dialIP
}

// pcp protocol server struct
type PcpProtocolConnection struct {
	pcpServerObj       *PcpServer
	ServerAddr         net.Addr
	Connection         net.PacketConn
	OutputChan         chan *lib.PcpPacket
	ServiceMap         map[int]*Service
	serviceCloseSignal chan *Service
}

func NewPcpServer(protocolId uint8) (*PcpServer, error) {
	// starts the PCP protocol client main service
	// the main role is to create pcpServer object - one per system

	// create map for pcpServerProtocolConnection
	protoConnectionMap := make(map[string]*PcpProtocolConnection)

	pcpServerObj := &PcpServer{
		ProtocolID:         protocolId,
		ProtoConnectionMap: protoConnectionMap,
	}

	lib.Pool = lib.NewPayloadPool(config.AppConfig.PayloadPoolSize, config.AppConfig.PreferredMSS)
	fmt.Println("Pcp protocol client started")

	return pcpServerObj, nil
}

func newPcpServerProtocolConnection(p *PcpServer, serverIP string) (*PcpProtocolConnection, error) { // serverIP must be a specific IP, not 0.0.0.0
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
		pcpServerObj:       p,
		ServerAddr:         serverAddr,
		Connection:         protocolConn,
		OutputChan:         make(chan *lib.PcpPacket),
		ServiceMap:         make(map[int]*Service),
		serviceCloseSignal: make(chan *Service),
	}

	// Start goroutines to handle incoming and outgoing packets
	go pcpObj.handlingIncomingPackets()
	go pcpObj.handleOutgoingPackets()

	return pcpObj, nil
}

func (p *PcpProtocolConnection) handlingIncomingPackets() {
	// Continuously read from the protocolConn
	buffer := make([]byte, config.AppConfig.PreferredMSS+lib.TcpHeaderLength+lib.TcpOptionsMaxLength+lib.TcpPseudoHeaderLength)
	pcpFrame := buffer[lib.TcpPseudoHeaderLength:] // the first lib.TcpPseudoHeaderLength bytes are reserved for Tcp pseudo header
	for {
		n, addr, err := p.Connection.ReadFrom(pcpFrame)
		if err != nil {
			fmt.Println("Error reading:", err)
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
		destPort := packet.DestinationPort
		//log.Printf("Got packet with options: %+v\n", packet.TcpOptions)

		// Check if a connection is registered for the packet
		config.Mu.Lock()
		service, ok := p.ServiceMap[int(destPort)]
		config.Mu.Unlock()

		if !ok {
			//fmt.Println("No service registered for port:", destPort)
			continue
		}

		// Dispatch the packet to the corresponding service's input channel
		service.InputChannel <- packet
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *PcpProtocolConnection) handleOutgoingPackets() {
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
		if packet.IsOpenConnection && config.AppConfig.PacketLostSimulation && !packet.Conn.WriteOnHold {
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

		if packet.IsOpenConnection && config.AppConfig.PacketLostSimulation {
			count = (count + 1) % 10
		}
		packetLost = false
	}
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
		pConn, err = newPcpServerProtocolConnection(p, normServiceIpString)
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
		srv, err := newService(pConn, serviceAddr, port, pConn.OutputChan)
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

		go srv.handleServicePackets()
		go srv.handleCloseConnections()

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
