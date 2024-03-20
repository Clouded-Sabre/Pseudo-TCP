package server

import (
	"fmt"
	"log"
	"net"
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
	OutputChan         chan *lib.PacketVector
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
		OutputChan:         make(chan *lib.PacketVector),
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
		if !lib.VerifyChecksum(packetData, addr, p.ServerAddr, p.pcpServerObj.ProtocolID) {
			log.Println("Packet checksum verification failed. Skip this packet.")
			continue
		}

		// Extract destination port
		packet := &lib.PcpPacket{}
		packet.Unmarshal(packetData)
		destPort := packet.DestinationPort

		// Check if a connection is registered for the packet
		config.Mu.Lock()
		service, ok := p.ServiceMap[int(destPort)]
		config.Mu.Unlock()

		if !ok {
			fmt.Println("No service registered for port:", destPort)
			continue
		}

		// Dispatch the packet to the corresponding service's input channel
		service.InputChannel <- &lib.PacketVector{Data: packet, RemoteAddr: addr, LocalAddr: p.ServerAddr}
	}
}

// handleOutgoingPackets handles outgoing packets by writing them to the interface.
func (p *PcpProtocolConnection) handleOutgoingPackets() {
	for {
		packet := <-p.OutputChan // Subscribe to p.OutputChan
		// Marshal the packet into bytes
		fmt.Printf("Sending payload: %+v\n", packet.Data)
		frameBytes := packet.Data.Marshal(packet.LocalAddr, packet.RemoteAddr, p.pcpServerObj.ProtocolID)
		fmt.Println("Frame Length:", len(frameBytes), frameBytes)
		// Write the packet to the interface
		_, err := p.Connection.WriteTo(frameBytes, packet.RemoteAddr)
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
	srv, ok := pConn.ServiceMap[port]
	if !ok {
		// need to create new service
		srv, err = newService(pConn, serviceAddr, port, pConn.OutputChan)
		if err != nil {
			log.Println("Error creating service:", err)
			return nil, err
		}
		// add it to ServiceMap
		pConn.ServiceMap[port] = srv
	}

	go srv.handleServicePackets()
	go srv.handleCloseConnections()

	return srv, nil
}
