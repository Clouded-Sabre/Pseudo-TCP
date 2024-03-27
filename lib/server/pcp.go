package server

import (
	"fmt"
	"log"
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

	pcpServerObj := &PcpServer{ProtocolID: protocolId, ProtoConnectionMap: protoConnectionMap}

	fmt.Println("Pcp protocol client started")

	// Start a goroutine to periodically check protocolConn and connection health
	//go checkServiceHealth(services)

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

	// Start a goroutine to periodically check service health
	//go checkServiceHealth(services)

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
	buffer := make([]byte, 1024)
	for {
		n, addr, err := p.Connection.ReadFrom(buffer)
		if err != nil {
			fmt.Println("Error reading:", err)
			continue
		}

		// Make a copy of the packet data
		packetData := make([]byte, n)
		copy(packetData, buffer[:n])

		// check PCP packet checksum
		/*if !lib.VerifyChecksum(packetData, addr, p.ServerAddr, p.pcpServerObj.ProtocolID) {
			log.Println("Packet checksum verification failed. Skip this packet.")
			continue
		}*/

		// Extract destination port
		packet := &lib.PcpPacket{}
		packet.Unmarshal(packetData, addr, p.ServerAddr)
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
	for {
		packet := <-p.OutputChan // Subscribe to p.OutputChan
		// Marshal the packet into bytes
		frameBytes := packet.Marshal(p.pcpServerObj.ProtocolID)
		// Write the packet to the interface
		_, err := p.Connection.WriteTo(frameBytes, packet.DestAddr)
		if err != nil {
			fmt.Println("Error writing packet:", err)
			continue
		}
	}
}

// checkServiceHealth periodically checks the health of registered services
// and terminates any services whose parent process has terminated.
/*func checkServiceHealth(services map[int]*lib.Service) {
	for {
		for port, srv := range services {
			// Check if the parent process is alive
			if !srv.IsParentAlive() {
				fmt.Println("Parent process terminated for service on port:", port)
				// Terminate the service
				srv.Stop()
				delete(services, port)
			}
		}
		// Sleep for some duration before checking again
		time.Sleep(5 * time.Second)
	}
}*/

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
