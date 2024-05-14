package client

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rp "github.com/Clouded-Sabre/ringpool/lib"
)

// pcp protocol client struct
type pcpClient struct {
	ProtocolID           uint8
	ProtoConnectionMap   map[string]*pcpProtocolConnection // keep track of all protocolConn created by dialIP
	pConnCloseSignalChan chan *pcpProtocolConnection       // signal for pcpProtocolConnection close
	wg                   sync.WaitGroup                    // WaitGroup to synchronize goroutines
	closeSignal          chan struct{}                     // used to send close signal to go routines to stop when timeout arrives
}

func NewPcpClient(protocolId uint8) *pcpClient {
	// starts the PCP protocol client main service
	// the main role is to create pcpClient object - one per system

	// Output channel for writing packets to the interface
	protoConnectionMap := make(map[string]*pcpProtocolConnection)

	pcpClientObj := &pcpClient{
		ProtocolID:           protocolId,
		ProtoConnectionMap:   protoConnectionMap,
		pConnCloseSignalChan: make(chan *pcpProtocolConnection),
		wg:                   sync.WaitGroup{},
		closeSignal:          make(chan struct{}),
	}

	// Preparing Ring Buffer Pool
	rp.Debug = true
	lib.Pool = rp.NewRingPool(config.AppConfig.PayloadPoolSize, lib.NewPayload, config.AppConfig.PreferredMSS)

	// Start a goroutine to periodically check protocolConn and connection health
	pcpClientObj.wg.Add(1) // Increase WaitGroup counter by 1 for the handlePConnClose goroutines
	go pcpClientObj.handlePConnClose()

	log.Println("Pcp protocol client started")

	return pcpClientObj
}

func (p *pcpClient) handlePConnClose() {
	// Decrease WaitGroup counter when the goroutine completes
	defer p.wg.Done()

	for {
		select {
		case <-p.closeSignal:
			return // close the go routine gracefully
		case pConn := <-p.pConnCloseSignalChan:
			// clear it from p.ConnectionMap
			_, ok := p.ProtoConnectionMap[pConn.pConnKey]
			if !ok {
				// connection does not exist in ConnectionMap
				log.Printf("Pcp Protocol connection does not exist in %s->%s", pConn.LocalAddr.IP.String(), pConn.ServerAddr.IP.String())
				continue
			}

			// delete the clientConn from ConnectionMap
			delete(p.ProtoConnectionMap, pConn.pConnKey)
			log.Printf("Pcp Protocol connection %s->%s terminated and removed.", pConn.LocalAddr.IP.String(), pConn.ServerAddr.IP.String())
		}
	}
}

// dialPcp simulates the TCP dial function interface for PCP.
func (p *pcpClient) DialPcp(localIP string, serverIP string, serverPort uint16) (*lib.Connection, error) {
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
		pConn, err = newPcpProtocolConnection(pConnKey, p, serverAddr, localAddr, p.pConnCloseSignalChan)
		if err != nil {
			fmt.Println("Error creating Pcp Client Protocol Connection:", err)
			return nil, err
		}
		// add it to ProtoConnectionMap
		p.ProtoConnectionMap[pConnKey] = pConn
	}

	newClientConn, err := pConn.dial(int(serverPort))
	if err != nil {
		fmt.Println("Error creating Pcp Client Connection:", err)
		return nil, err
	}

	return newClientConn, nil
}

// close the pcpClient
func (p *pcpClient) Close() {
	// Close all pcpProtocolConnection instances
	for _, pConn := range p.ProtoConnectionMap {
		pConn.Close()
	}
	p.ProtoConnectionMap = nil // Clear the map after closing all connections

	// Send closeSignal to all goroutines
	close(p.closeSignal)

	// Wait for all goroutines to finish
	p.wg.Wait()

	close(p.pConnCloseSignalChan)

	log.Println("PcpClient closed gracefully.")
}
