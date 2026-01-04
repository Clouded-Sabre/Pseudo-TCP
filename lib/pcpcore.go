package lib

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	rp "github.com/Clouded-Sabre/ringpool/lib"

	"sync"
)

type PcpCoreConfig struct {
	ProtocolID           uint8 // protocol id which should be 6
	PayloadPoolSize      int   // how many number of packet payload chunks in the pool
	PreferredMSS         int   // preferred MSS
	Debug                bool  // global debug setting
	PoolDebug            bool  // Ring Pool debug setting
	ProcessTimeThreshold int   // packet processing time threshold
}

type PcpCore struct {
	config             *PcpCoreConfig                    // config
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
		pConn, err = newPcpProtocolConnection(pConnKey, false, int(p.config.ProtocolID), serverAddr, localAddr, p.pConnCloseSignal, pcpConnConfig)
		if err != nil {
			fmt.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.protoConnectionMap[pConnKey] = pConn
	} else {
		// Check if the existing PcpProtocolConnection is still valid (not closed)
		pConn.mu.Lock()
		isClosed := pConn.isClosed
		pConn.mu.Unlock()

		if isClosed {
			// The protocol connection is closed, remove it from map and create a new one
			log.Printf("PcpProtocolConnection %s is closed, creating new one\n", pConnKey)
			delete(p.protoConnectionMap, pConnKey)

			pConn, err = newPcpProtocolConnection(pConnKey, false, int(p.config.ProtocolID), serverAddr, localAddr, p.pConnCloseSignal, pcpConnConfig)
			if err != nil {
				fmt.Println("Error creating new Pcp Client Protocol Connection:", err)
				return nil, err
			}
			// add it to ProtoConnectionMap
			p.protoConnectionMap[pConnKey] = pConn
		}
	}

	newClientConn, err := pConn.dial(int(serverPort), pcpConnConfig.connConfig)
	if err != nil {
		// If dial failed, check if we should close and recreate the protocol connection
		// This handles cases where the underlying ipConn is broken
		pConn.mu.Lock()
		isClosed := pConn.isClosed
		pConn.mu.Unlock()

		if !isClosed {
			log.Printf("PcpProtocolConnection.dial failed: %v. Attempting to close and recreate protocol connection\n", err)
			pConn.Close()
			delete(p.protoConnectionMap, pConnKey)

			// Try one more time with a fresh protocol connection
			pConn, err = newPcpProtocolConnection(pConnKey, false, int(p.config.ProtocolID), serverAddr, localAddr, p.pConnCloseSignal, pcpConnConfig)
			if err != nil {
				fmt.Println("Error creating new Pcp Client Protocol Connection after failure:", err)
				return nil, err
			}
			p.protoConnectionMap[pConnKey] = pConn

			// Try dial again with the new protocol connection
			newClientConn, err = pConn.dial(int(serverPort), pcpConnConfig.connConfig)
			if err != nil {
				fmt.Println("Error creating Pcp Client Connection on retry:", err)
				return nil, err
			}
		} else {
			fmt.Println("Error creating Pcp Client Connection:", err)
			return nil, err
		}
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
		pConn, err = newPcpProtocolConnection(pConnKey, true, int(p.config.ProtocolID), serviceAddr, nil, p.pConnCloseSignal, pcpConnConfig)
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

		// Add firewall rule to drop RST packets created by system TCP/IP network stack (using abstraction layer - supports both iptables and nftables)
		if err := pConn.packetFilterer.AddRule(serviceIP, port, "server"); err != nil {
			log.Println("Error adding firewall rule:", err)
			return nil, err
		}

		SleepForMs(500) // sleep for 500ms to make sure iptables rule takes effect

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
		if pConn.isServer {
			// remove firewall rules for server protocol connection (using abstraction layer - supports both iptables and nftables)
			for port := range pConn.iptableRules {
				err := pConn.packetFilterer.RemoveRule(pConn.serverAddr.IP.String(), port, "server")
				if err != nil {
					log.Println(err)
					return err
				}
			}
		}
	}
	p.protoConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	p.wg.Wait()

	close(p.pConnCloseSignal)

	log.Println("Pcp core closed gracefully.")

	return nil
}

// addServerIptablesRule adds an iptables rule to drop RST packets originating from the given IP and port.
// Deprecated: Use PacketFilterer.AddRule() instead, which supports both iptables and nftables.
func addServerIptablesRule(ip string, port int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// removeServerIptablesRule removes the iptables rule that was added for dropping RST packets.
// Deprecated: Use PacketFilterer.RemoveRule() instead, which supports both iptables and nftables.
func removeServerIptablesRule(ip string, port int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		// If there is an error executing the command, return the error
		return err
	}

	return nil
}
