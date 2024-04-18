package client

import (
	"fmt"
	"net"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// pcp protocol client struct
type pcpClient struct {
	ProtocolID         uint8
	ProtoConnectionMap map[string]*pcpProtocolConnection // keep track of all protocolConn created by dialIP
}

func NewPcpClient(protocolId uint8) *pcpClient {
	// starts the PCP protocol client main service
	// the main role is to create pcpClient object - one per system

	// Output channel for writing packets to the interface
	protoConnectionMap := make(map[string]*pcpProtocolConnection)

	pcpClientObj := &pcpClient{ProtocolID: protocolId, ProtoConnectionMap: protoConnectionMap}

	lib.Pool = lib.NewPayloadPool(config.AppConfig.PayloadPoolSize, config.AppConfig.PreferredMSS)

	fmt.Println("Pcp protocol client started")

	// Start a goroutine to periodically check protocolConn and connection health
	//go checkServiceHealth(services)

	return pcpClientObj
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
		pConn, err = newPcpProtocolConnection(p, serverAddr, localAddr)
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
