package main

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/net/ipv4"
)

func main() {
	// Listen on port 12345 (replace with desired port)
	ln, err := ipv4.Listen(net.ParseIP("0.0.0.0"), 12345, ipv4.ProtocolTCP)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer ln.Close()

	for {
		// Accept connection
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println(err)
			continue
		}
		defer conn.Close()

		// Simulate TCP SYN-ACK (flags: SYN, ACK)
		err = conn.SetDeadline(time.Now().Add(time.Second * 5)) // Set timeout for receiving SYN
		if err != nil {
			fmt.Println(err)
			continue
		}
		b := make([]byte, ipv4.HeaderLen)
		n, _, err := conn.Read(b)
		if err != nil {
			fmt.Println(err)
			continue
		}
		if n == ipv4.HeaderLen && b[ipv4.TCPFlagOffset]&(ipv4.TCPFlagSYN|ipv4.TCPFlagACK) == (ipv4.TCPFlagSYN|ipv4.TCPFlagACK) {
			fmt.Println("Received SYN from client")
			b[ipv4.TCPFlagOffset] &= ^ipv4.TCPFlagSYN // Remove SYN flag
			_, err = conn.Write(b)                    // Send simulated SYN-ACK
			if err != nil {
				fmt.Println(err)
				continue
			}
		} else {
			fmt.Println("Unexpected packet received")
			continue
		}

		// Simulate receiving data (replace with actual handling)
		for {
			b := make([]byte, 1024)
			n, _, err := conn.Read(b)
			if err != nil {
				fmt.Println(err)
				break
			}
			fmt.Printf("Received %d bytes from client: %s\n", n, string(b[:n]))
		}
	}
}
