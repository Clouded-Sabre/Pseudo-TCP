package lib

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"

	rp "github.com/Clouded-Sabre/ringpool/lib"

	"sync"
)

type PcpCoreConfig struct {
	ProtocolID      uint8 // protocol id which should be 6
	PayloadPoolSize int   // how many number of packet payload chunks in the pool
	PreferredMSS    int   // preferred MSS
}

type PcpCore struct {
	ProtocolID         uint8
	ProtoConnectionMap map[string]*PcpProtocolConnection // keep track of all protocolConn created by dialIP
	pConnCloseSignal   chan *PcpProtocolConnection
	closeSignal        chan struct{}  // used to send close signal to go routines to stop when timeout arrives
	wg                 sync.WaitGroup // WaitGroup to synchronize goroutines
}

func NewPcpCore(pcpcoreConfig *PcpCoreConfig) (*PcpCore, error) {
	// starts the PCP core main service
	// the main role is to create PcpCore object - one per system
	pcpServerObj := &PcpCore{
		ProtocolID:         uint8(pcpcoreConfig.ProtocolID),
		ProtoConnectionMap: make(map[string]*PcpProtocolConnection),
		pConnCloseSignal:   make(chan *PcpProtocolConnection),
		closeSignal:        make(chan struct{}),
	}

	rp.Debug = true
	Pool = rp.NewRingPool(pcpcoreConfig.PayloadPoolSize, NewPayload, pcpcoreConfig.PreferredMSS)

	// Start goroutines
	pcpServerObj.wg.Add(1) // Increase WaitGroup counter by 1 for the handleClosePConnConnection goroutines
	go pcpServerObj.handleClosePConnConnection()

	fmt.Println("Pcp protocol core started")

	return pcpServerObj, nil
}

// dialPcp simulates the TCP dial function interface for PCP.
func (p *PcpCore) DialPcp(localIP string, serverIP string, serverPort uint16, pcpConfig *PcpProtocolConnConfig) (*Connection, error) {
	// first normalize IP address string before making key
	serverAddr, err := net.ResolveIPAddr("ip", serverIP)
	if err != nil {
		return nil, err
	}

	localAddr, err := net.ResolveIPAddr("ip", localIP)
	if err != nil {
		return nil, err
	}

	pConnKey := fmt.Sprintf("%s-%s", serverAddr.IP.To4().String(), localAddr.IP.To4().String())
	// Check if the connection exists in the connection map
	pConn, ok := p.ProtoConnectionMap[pConnKey]
	if !ok {
		// need to create new protocol connection
		pConn, err = newPcpProtocolConnection(pConnKey, false, int(p.ProtocolID), serverAddr, localAddr, p.pConnCloseSignal, pcpConfig)
		if err != nil {
			fmt.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.ProtoConnectionMap[pConnKey] = pConn
	}

	newClientConn, err := pConn.dial(int(serverPort), pcpConfig.ConnConfig)
	if err != nil {
		fmt.Println("Error creating Pcp Client Connection:", err)
		return nil, err
	}

	return newClientConn, nil
}

// ListenPcp starts listening for incoming packets on the service's port.
func (p *PcpCore) ListenPcp(serviceIP string, port int, pcpConfig *PcpProtocolConnConfig) (*Service, error) {
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
		pConn, err = newPcpProtocolConnection(pConnKey, true, int(p.ProtocolID), serviceAddr, nil, p.pConnCloseSignal, pcpConfig)
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
		srv, err := newService(pConn, serviceAddr, port, pConn.OutputChan, pConn.sigOutputChan, pConn.serviceCloseSignal, pcpConfig.ConnConfig)
		if err != nil {
			log.Println("Error creating service:", err)
			return nil, err
		}

		// Add iptables rule to drop RST packets created by system TCP/IP network stack
		if err := addServerIptablesRule(serviceIP, port); err != nil {
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
func addServerIptablesRule(ip string, port int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// removeIptablesRule removes the iptables rule that was added for dropping RST packets.
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

func (p *PcpCore) handleClosePConnConnection() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return // gracefully stop the go routine
		case pConn := <-p.pConnCloseSignal:
			// clear it from p.ConnectionMap
			_, ok := p.ProtoConnectionMap[pConn.Key] // just make sure it really in ConnectionMap for debug purpose
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Protocol Connection %s does not exist in service map", pConn.ServerAddr.IP.String())
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.ProtoConnectionMap, pConn.Key)
			log.Printf("Pcp protocol connection %s terminated and removed.", pConn.ServerAddr.IP.String())
		}
	}
}

func (p *PcpCore) Close() error {
	// Close all pcpProtocolConnection instances
	for _, pConn := range p.ProtoConnectionMap {
		pConn.Close()
		if pConn.isServer {
			// remove iptable rules for server protocol connection
			for port := range pConn.iptableRules {
				err := removeServerIptablesRule(pConn.ServerAddr.IP.String(), port)
				if err != nil {
					log.Println(err)
					return err
				}
			}
		}
	}
	p.ProtoConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	p.wg.Wait()

	close(p.pConnCloseSignal)

	log.Println("Pcp core closed gracefully.")

	return nil
}
