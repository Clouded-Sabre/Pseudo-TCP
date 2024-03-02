package main

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/net/ipv4"
)

func main() {
	// Connect to server (replace with server address and port)
	conn, err := ipv4.Dial(net.ParseIP("127.0.0.1"), 12345, ipv4.ProtocolTCP)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()

	// Simulate TCP SYN (flags: SYN)
	b := make([]byte, ipv4.HeaderLen)
	b[ipv4.TCPFlagOffset] = ipv4.TCPFlagSYN
	_, err = conn.Write(b)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Simulate receiving SYN-ACK (replace with actual data sending)
	err = conn.SetDeadline(time.Now().Add(time.Second * 5)) // Set timeout for receiving SYN-ACK
	if err != nil {
		fmt.Println(err)
		return
	}
	b = make([]byte, ipv4.HeaderLen)
	n, _, err := conn.Read(b)
	if err != nil {
		fmt.Println(err)
		return
	}
	if n == ipv4.HeaderLen && b[ipv4.TCPFlagOffset]&ipv4.TCPFlagACK != 0 {
		fmt.Println("Received SYN-ACK from server")
	} else {
		fmt.Println("Unexpected packet received")
		return
	}

	// Send data (replace with your data)
	data := []byte("Hello from client!")
	_, err = conn.Write(data)

}
