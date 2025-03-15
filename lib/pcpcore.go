package lib

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	rs "github.com/Clouded-Sabre/rawsocket/lib"
	rp "github.com/Clouded-Sabre/ringpool/lib"

	"sync"
)

type PcpCoreConfig struct {
	ProtocolID           uint8        // protocol id which should be 6
	PayloadPoolSize      int          // how many number of packet payload chunks in the pool
	PreferredMSS         int          // preferred MSS
	Debug                bool         // global debug setting
	PoolDebug            bool         // Ring Pool debug setting
	ProcessTimeThreshold int          // packet processing time threshold
	RsConfig             *rs.RsConfig // rawsocket configuration
}

func NewDefaultPcpCoreConfig() *PcpCoreConfig {
	return &PcpCoreConfig{
		ProtocolID:           6,
		PayloadPoolSize:      2000,
		PreferredMSS:         1440,
		Debug:                false,
		PoolDebug:            false,
		ProcessTimeThreshold: 10,
		RsConfig:             rs.NewDefaultRsConfig(),
	}
}

type PcpCore struct {
	config             *PcpCoreConfig                    // config
	rscore             rs.RSCore                         // used for macos and windows only
	protoConnectionMap map[string]*PcpProtocolConnection // keep track of all protocolConn created by dialIP
	pConnCloseSignal   chan *PcpProtocolConnection
	closeSignal        chan struct{}  // used to send close signal to go routines to stop when timeout arrives
	wg                 sync.WaitGroup // WaitGroup to synchronize goroutines
}

func NewPcpCore(pcpcoreConfig *PcpCoreConfig) (*PcpCore, error) {
	// starts the PCP core main service
	// the main role is to create PcpCore object - one per system
	pcpServerObj := &PcpCore{
		config:             pcpcoreConfig,
		protoConnectionMap: make(map[string]*PcpProtocolConnection),
		pConnCloseSignal:   make(chan *PcpProtocolConnection),
		closeSignal:        make(chan struct{}),
	}

	rp.Debug = pcpcoreConfig.PoolDebug
	Pool = rp.NewRingPool("PCP: ", pcpcoreConfig.PayloadPoolSize, NewPayload, pcpcoreConfig.PreferredMSS)
	Pool.Debug = pcpcoreConfig.PoolDebug
	Pool.ProcessTimeThreshold = time.Duration(pcpcoreConfig.ProcessTimeThreshold) * time.Millisecond

	// create RSCore object for rawsocket
	var err error
	pcpServerObj.rscore, err = rs.NewRSCore(pcpcoreConfig.RsConfig)
	if err != nil {
		log.Fatal("Error creating RSCore object:", err)
	}
	// Start goroutines
	pcpServerObj.wg.Add(1) // Increase WaitGroup counter by 1 for the handleClosePConnConnection goroutines
	go pcpServerObj.handleClosePConnConnection()

	log.Println("Pcp protocol core started")

	return pcpServerObj, nil
}

// dialPcp simulates the TCP dial function interface for PCP.
func (p *PcpCore) DialPcp(localIP string, serverIP string, serverPort uint16, pcpConfig *config.Config) (*Connection, error) {
	// first normalize IP address string before making key
	serverAddr, err := net.ResolveIPAddr("ip", serverIP)
	if err != nil {
		return nil, err
	}

	localAddr, err := net.ResolveIPAddr("ip", localIP)
	if err != nil {
		return nil, err
	}

	pcpConnConfig := newPcpProtocolConnConfig(pcpConfig)
	pConnKey := fmt.Sprintf("%s-%s", serverAddr.IP.To4().String(), localAddr.IP.To4().String())
	// Check if the connection exists in the connection map
	pConn, ok := p.protoConnectionMap[pConnKey]
	if !ok {
		// need to create new protocol connection
		pConn, err = newPcpProtocolConnection(p, pConnKey, false, int(p.config.ProtocolID), serverAddr, localAddr, p.pConnCloseSignal, pcpConnConfig)
		if err != nil {
			fmt.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.protoConnectionMap[pConnKey] = pConn
	}

	newClientConn, err := pConn.dial(int(serverPort), pcpConnConfig.connConfig)
	if err != nil {
		fmt.Println("Error creating Pcp Client Connection:", err)
		return nil, err
	}

	return newClientConn, nil
}

// ListenPcp starts listening for incoming packets on the service's port.
func (p *PcpCore) ListenPcp(serviceIP string, port int, pcpConfig *config.Config) (*Service, error) {
	// first check if corresponding PcpServerProtocolConnection obj exists or not
	// Normalize IP address string before making key from it
	serviceAddr, err := net.ResolveIPAddr("ip", serviceIP)
	if err != nil {
		log.Println("IP address is malformated:", err)
		return nil, err
	}
	normServiceIpString := serviceAddr.IP.To4().String()

	pcpConnConfig := newPcpProtocolConnConfig(pcpConfig)
	pConnKey := normServiceIpString
	// Check if the connection exists in the connection map
	pConn, ok := p.protoConnectionMap[pConnKey]
	if !ok {
		// need to create new protocol connection
		pConn, err = newPcpProtocolConnection(p, pConnKey, true, int(p.config.ProtocolID), serviceAddr, nil, p.pConnCloseSignal, pcpConnConfig)
		if err != nil {
			log.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.protoConnectionMap[pConnKey] = pConn
	}

	// then we need to check if there is already a service listening at that serviceIP and port
	pConn.mu.Lock()
	_, ok = pConn.serviceMap[port]
	pConn.mu.Unlock()
	if !ok {
		// need to create new service
		// create new Pcp service
		srv, err := newService(pConn, serviceAddr, port, pConn.outputChan, pConn.sigOutputChan, pConn.serviceCloseSignal, pcpConnConfig.connConfig)
		if err != nil {
			log.Println("Error creating service:", err)
			return nil, err
		}

		// Add a dumb TCP server to prevent RST packets created by system TCP/IP network stack
		if srv.dumbListener, err = setupDumbTcpServer(serviceIP, port); err != nil {
			log.Println("Error adding dumb TCP server:", err)
			return nil, err
		}

		// add it to ServiceMap
		pConn.mu.Lock()
		pConn.serviceMap[port] = srv
		pConn.mu.Unlock()

		return srv, nil
	} else {
		err = fmt.Errorf("%s:%d is already taken", serviceIP, port)
		return nil, err
	}
}

func (p *PcpCore) handleClosePConnConnection() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return // gracefully stop the go routine
		case pConn := <-p.pConnCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.protoConnectionMap[pConn.key] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Protocol Connection %s does not exist in service map", pConn.serverAddr.IP.String())
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.protoConnectionMap, pConn.key)
			log.Printf("Pcp protocol connection %s terminated and removed.", pConn.serverAddr.IP.String())
		}
	}
}

func (p *PcpCore) Close() error {
	// Close all pcpProtocolConnection instances
	for _, pConn := range p.protoConnectionMap {
		pConn.Close()
	}
	p.protoConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	p.wg.Wait()

	close(p.pConnCloseSignal)

	finishFiltering() // finish filtering RST packet by removing any remaining filtering rules

	if p.rscore != nil {
		err := p.rscore.Close()
		if err != nil {
			log.Println("Error closing RSCore:", err)
			return err
		}
	}

	log.Println("Pcp core closed gracefully.")

	return nil
}

// setupDumbTcpServer adds an dumb tcp server at a specified ip:port to prevent RST packets originating from the given IP and port.
func setupDumbTcpServer(ip string, port int) (*net.TCPListener, error) {
	// Create a TCP socket and bind it to the desired IP address and port
	address := fmt.Sprintf("%s:%d", ip, port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener on %s: %v", address, err)
	}

	// Don't accept any connections, just call Listen()
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("failed to cast listener to TCPListener")
	}

	// This makes the kernel aware of the port and prevents RST from being sent
	tcpListener.SetDeadline(time.Now().Add(1 * time.Second)) // optional, just to make it a valid listener

	// Return the listener
	return tcpListener, nil
}
