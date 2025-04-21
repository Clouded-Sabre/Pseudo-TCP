package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rs "github.com/Clouded-Sabre/rawsocket/lib"
)

var (
	serverIP   string
	serverPort int
	sshServer  string
)

func init() {
	// Command-line flags
	flag.StringVar(&serverIP, "ip", "127.0.0.2", "Server IP address")
	flag.IntVar(&serverPort, "port", 8901, "Server port number")
	flag.StringVar(&sshServer, "sshServer", "127.0.0.1:22", "SSH server address")
	flag.Parse()
}

func main() {
	// Load PCP configuration
	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Configuration file error: %v", err)
	}

	// Create raw socket core
	defaultRsConf := rs.DefaultRsConfig()
	rscore, err := rs.NewRSCore(defaultRsConf)
	if err != nil {
		log.Fatalf("Failed to create rawsocket core: %v", err)
	}
	defer rscore.Close()

	// Create PCP core
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig, &rscore, "PCP_anchor")
	if err != nil {
		log.Fatalf("Error creating PCP core: %v", err)
	}
	defer pcpCoreObj.Close()

	// Start PCP server
	srv, err := pcpCoreObj.ListenPcp(serverIP, serverPort, connConfig)
	if err != nil {
		log.Fatalf("PCP server error listening at %s:%d: %v", serverIP, serverPort, err)
	}
	log.Printf("PCP service started at %s:%d", serverIP, serverPort)

	// Handle graceful shutdown
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	closeChan := make(chan struct{})
	var wg sync.WaitGroup

	go func() {
		<-signalChan
		log.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(closeChan)
		srv.Close()
	}()

	// Accept a single PCP connection (for simplicity)
	conn, err := srv.Accept()
	if err != nil {
		log.Printf("Error accepting PCP connection: %v", err)
		return
	}
	log.Println("PCP client connected")

	wg.Add(1)
	go handlePCPConnection(conn, closeChan, &wg)

	wg.Wait()
	log.Println("Server exiting...")
}

func handlePCPConnection(pcpConn *lib.Connection, closeChan chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	defer pcpConn.Close()

	// Dial TCP connection to SSH server
	sshConn, err := net.Dial("tcp", sshServer)
	if err != nil {
		log.Printf("Error connecting to SSH server: %v", err)
		return
	}
	defer sshConn.Close()

	// Send confirmation to forwarder client
	_, err = pcpConn.Write([]byte("TCP_CONNECTED"))
	if err != nil {
		log.Printf("Error sending confirmation: %v", err)
		return
	}
	log.Println("Sent TCP connection confirmation to client")

	// Forward data bi-directionally
	var forwardWg sync.WaitGroup
	forwardWg.Add(2)

	// Upstream: PCP connection -> SSH server
	go func() {
		defer forwardWg.Done()
		_, err := io.Copy(sshConn, pcpConn)
		if err != nil {
			log.Printf("Error forwarding PCP to SSH: %v", err)
		}
	}()

	// Downstream: SSH server -> PCP connection
	go func() {
		defer forwardWg.Done()
		_, err := io.Copy(pcpConn, sshConn)
		if err != nil {
			log.Printf("Error forwarding SSH to PCP: %v", err)
		}
	}()

	// Monitor closeChan to stop forwarding on shutdown
	go func() {
		<-closeChan
		log.Println("Received shutdown signal, closing connections")
		pcpConn.Close()
		sshConn.Close()
	}()

	forwardWg.Wait()
}
