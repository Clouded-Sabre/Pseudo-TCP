package filter

import (
	"fmt"
	"log"
	"net"
	"sync"
)

type Filter interface {
	AddTcpClientFiltering(dstAddr string, dstPort int) error    // adds a TCP filtering rule on client side to block RST packets sent out to server.
	RemoveTcpClientFiltering(dstAddr string, dstPort int) error // removes a TCP filtering rule on client side to block RST packets sent out to server.                                // finishFiltering flushes all rules.
	AddTcpServerFiltering(srtAddr string, srtPort int) error    // adds a TCP filtering rule on server side to block RST packets sent from server's listening port.
	RemoveTcpServerFiltering(srtAddr string, srtPort int) error // removes a TCP filtering rule on server side to block RST packets sent from server's listening port.
	FinishFiltering() error                                     // flushes all rules and stop filtering.
	AddUdpServerFiltering(srcAddr string) error                 // adds a UDP filtering rule on server side which blocks icmp unreacheable packets from srcAddr.
	RemoveUdpServerFiltering(srcAddr string) error              // removes a UDP filtering rule on server side which blocks icmp unreacheable packets from srcAddr.
	AddUdpClientFiltering(dstAddr string) error                 // adds a filtering rule on client side which block icmp unreacheable packets to dstAddr on server.
	RemoveUdpClientFiltering(dstAddr string) error              // removes a filtering rule on client side which blocks icmp unreacheable packets to dstAddr on server.
}

// Struct containing the shared method
type udpServerFilter struct {
	udpSrcMap sync.Map // Map to store UDP source addresses and ports to udp connections
}

func NewUdpServerFilter() *udpServerFilter {
	return &udpServerFilter{
		udpSrcMap: sync.Map{},
	}
}

func (u *udpServerFilter) AddUdpServerFiltering(srcAddr string) error { // Adds filtering to block icmp unreacheable packets from srcAddr.
	//srcAddr is the source ip address and port of the UDP server in "ip:port" format
	// start a dummy UDP server to prevent icmp port unreachable packets
	// Check if we already have a UDP server for this address
	if _, exists := u.udpSrcMap.Load(srcAddr); exists {
		// Server already exists, just increment the reference count
		return nil
	}

	// No existing server, create a new one
	udpAddr, err := net.ResolveUDPAddr("udp", srcAddr)
	if err != nil {
		return fmt.Errorf("invalid UDP address: %v", err)
	}

	// Create UDP connection
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to start UDP server: %v", err)
	}

	// Store the connection with initial reference count of 1
	u.udpSrcMap.Store(srcAddr, conn)

	log.Printf("Started the dummy UDP server at %s\n", srcAddr)
	return nil
}

func (u *udpServerFilter) RemoveUdpServerFiltering(srcAddr string) error { // Removes filtering to block icmp unreacheable packets from srcAddr.
	// srcAddr is the source ip address and port of the UDP server in "ip:port" format
	// removes the dummy udp server listening at the given IP and port.
	// Check if we have a UDP server for this address
	if conn, exists := u.udpSrcMap.Load(srcAddr); exists {
		// Server already exists, just increment the reference count
		conn.(*net.UDPConn).Close()
		log.Printf("Stopped the dummy UDP server at %s\n", srcAddr)
		return nil
	}

	// No existing server
	return nil
}
