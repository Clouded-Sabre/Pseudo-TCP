package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

var (
	svcAddrStr, svcIPstr, svcPortStr string
	svcPort                          int
	filePath                         string
	mtu                              int
	protocol                         string
	pcpCoreObj                       *lib.PcpCore
	err                              error
)

func init() {
	// Define CLI flags for server IP and port
	flag.StringVar(&svcAddrStr, "svcaddr", "127.0.0.1:8080", "SFDP service address(IP:Port)")
	flag.StringVar(&filePath, "file", "book.txt", "file path to the book txt file")
	flag.IntVar(&mtu, "mtu", 1300, "payload MTU")
	flag.StringVar(&protocol, "protocol", "udp", "Transport protocol: udp, tcp and pcp")
	flag.Parse()
}

func main() {
	switch protocol {
	case "udp":
		startUDPServer()
	case "tcp":
		startTCPServer()
	case "pcp":
		startPCPServer()
	default:
		fmt.Println("Invalid protocol specified. Use 'udp', 'tcp' and 'pcp'.")
		os.Exit(1)
	}
}

func startUDPServer() {
	udpAddr, err := net.ResolveUDPAddr("udp", svcAddrStr)
	if err != nil {
		fmt.Println("Error resolving UDP address:", err)
		os.Exit(1)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		fmt.Println("Error listening:", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Printf("UDP Server listening on %s\n", svcAddrStr)

	// Map to store connected clients
	connectedClients := make(map[string]chan []byte)

	for {
		// Accept incoming connections
		buffer := make([]byte, 8192)
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Println("Error reading UDP packet:", err)
			continue
		}

		clientAddr := addr.String()
		//log.Printf("Received packet from %s. Length: %d\n", clientAddr, n)
		//handleReceivedMessage(buffer[:n], addr.String())

		// Check if the client is already connected
		var (
			channel chan []byte
			ok      bool
		)
		if channel, ok = connectedClients[clientAddr]; !ok {
			// Spin off a new goroutine to handle the client
			inputChannel := make(chan []byte)
			go handleUdpInputFromClient(conn, addr, filePath, mtu, inputChannel)
			connectedClients[clientAddr] = inputChannel
			channel = inputChannel
		}

		channel <- buffer[:n]
	}
}

func startTCPServer() {
	listener, err := net.Listen("tcp", svcAddrStr)
	if err != nil {
		fmt.Println("Error starting TCP server:", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("TCP Server listening on %s\n", svcAddrStr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error accepting connection:", err)
			continue
		}
		//go handleOutputToClient(nil, conn, nil, filePath, mtu, maxGapMs)
		go handleTcpInputFromClient(conn, mtu)
	}
}

func startPCPServer() {
	svcIPstr, svcPortStr, err = net.SplitHostPort(svcAddrStr)
	if err != nil {
		log.Fatal(err)
	}
	svcPort, err = strconv.Atoi(svcPortStr)
	if err != nil {
		log.Fatal(err)
	}

	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	// Create PCP server
	pcpCoreObj, err = lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println("Error creating PCP core:", err)
		return
	}
	log.Println("PCP core started.")

	listener, err := pcpCoreObj.ListenPcp(svcIPstr, svcPort, connConfig)
	if err != nil {
		fmt.Println("Error starting PCP server:", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("PCP Server listening on %s\n", svcAddrStr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Error accepting connection:", err)
			continue
		}
		//go handleOutputToClient(nil, conn, nil, filePath, mtu, maxGapMs)
		go handlePcpInputFromClient(conn, mtu)
	}
}

func handleTcpInputFromClient(tcpConn net.Conn, mtu int) {
	buffer := make([]byte, mtu)
	file, err := os.Open(filePath)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	for {
		n, err := tcpConn.Read(buffer)

		if err != nil {
			log.Println("handleClientInputUDP:", err)
			return
		}
		//log.Printf("Got message from client(%s):%s\n", tcpConn.RemoteAddr().(*net.TCPAddr).String(), string(buffer[:n]))
		if n < 8 {
			log.Fatalf("Received message(%d bytes) is too short to contain timestamp(8 bytes)\n", n)
		}

		sendMessageBack(buffer[:n], nil, &tcpConn, nil, nil, mtu, file)
	}
}

func handlePcpInputFromClient(pcpConn *lib.Connection, mtu int) {
	buffer := make([]byte, mtu)
	file, err := os.Open(filePath)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	for {
		n, err := pcpConn.Read(buffer)

		if err != nil {
			log.Println("handleClientInputUDP:", err)
			return
		}
		//log.Printf("Got message from client(%s):%s\n", tcpConn.RemoteAddr().(*net.TCPAddr).String(), string(buffer[:n]))
		if n < 8 {
			log.Fatalf("Received message(%d bytes) is too short to contain timestamp(8 bytes)\n", n)
		}

		sendMessageBack(buffer[:n], nil, nil, pcpConn, nil, mtu, file)
	}
}

func handleUdpInputFromClient(conn *net.UDPConn, addr *net.UDPAddr, filePath string, mtu int, inputChannel chan []byte) {
	var message []byte
	file, err := os.Open(filePath)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	for {
		message = <-inputChannel
		if len(message) < 8 {
			log.Fatalf("Received message length(%d) is less than minimum value (8). Quit!\n", len(message))
		}
		//log.Printf("Got message from client(%s):%s\n", conn.RemoteAddr().String(), string(buffer[:n]))
		sendMessageBack(message, conn, nil, nil, addr, mtu, file)
	}
}

func sendMessageBack(message []byte, udpConn *net.UDPConn, tcpConn *net.Conn, pcpConn *lib.Connection, sAddr *net.UDPAddr, mtu int, file *os.File) {
	buffer := make([]byte, mtu)

	chunkSize := rand.Intn(mtu - 8) // Subtract 8 bytes for the timestamp

	// Read data from the file
	n, err := file.Read(buffer[8 : chunkSize+8])
	if err != nil {
		log.Fatalln("Error reading from file:", err)
	}

	// Prepend the timestamp to the buffer
	copy(buffer[:8], message[:8]) // including 4-byte sequence number and 8 bytes timestamp

	// Send the packet to the client
	if udpConn != nil {
		_, err = udpConn.WriteToUDP(buffer[:n+8], sAddr)
	} else if tcpConn != nil {
		_, err = (*tcpConn).Write(buffer[:n+8])
	} else {
		_, err = pcpConn.Write(buffer[:n+8])
	}
	if err != nil {
		log.Fatalln("Error sending packet:", err)
	}

	var sourceAddrStr string
	if udpConn != nil {
		sourceAddrStr = sAddr.String()
	} else if tcpConn != nil {
		sourceAddrStr = (*tcpConn).RemoteAddr().String()
	} else {
		sourceAddrStr = pcpConn.RemoteAddr().String()
	}
	log.Printf("Sent packet with length %d to %s\n", n+8, sourceAddrStr)

	if n < chunkSize {
		_, err = file.Seek(0, 0)
		if err != nil {
			log.Println("Error seeking to the beginning of the file:", err)
			return
		}
	}
}
