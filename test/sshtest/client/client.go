package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/filter"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rs "github.com/Clouded-Sabre/rawsocket/lib"
)

func main() {
	// In your main function or init()
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// Command-line flags
	localPort := flag.Int("localPort", 2222, "Local port to listen for SSH clients")
	serverIP := flag.String("serverIP", "127.0.0.2", "Forwarder server IP address")
	serverPort := flag.Int("serverPort", 8901, "Forwarder server port")
	flag.Parse()

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

	filter, err := filter.NewFilter("PCP_anchor")
	if err != nil {
		log.Fatal("Error creating filter object:", err)
	}
	// Create PCP core
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig, &rscore, &filter)
	if err != nil {
		log.Fatalf("Failed to create PCP core: %v", err)
	}
	defer pcpCoreObj.Close()

	// Listen for SSH client connection
	sshListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *localPort))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", *localPort, err)
	}
	defer sshListener.Close()

	log.Printf("Listening for SSH client on port %d", *localPort)

	// Accept a single SSH client connection (for simplicity)
	sshConn, err := sshListener.Accept()
	if err != nil {
		log.Fatalf("Failed to accept SSH client connection: %v", err)
	}
	log.Println("SSH client connected")

	// Handle the SSH client connection
	handleSSHClient(sshConn, pcpCoreObj, *serverIP, *serverPort, connConfig)
}

func handleSSHClient(sshConn net.Conn, pcpCoreObj *lib.PcpCore, serverIP string, serverPort int, connConfig *lib.ConnectionConfig) {
	defer sshConn.Close()

	// Dial PCP connection to forwarder server
	pcpConn, err := pcpCoreObj.DialPcp("", serverIP, uint16(serverPort), connConfig) // "" means let system choose the right local IP
	if err != nil {
		log.Printf("Error connecting to forwarder server: %v", err)
		return
	}
	defer pcpConn.Close()

	// Wait for confirmation from forwarder server
	buffer := make([]byte, 1024)
	n, err := pcpConn.Read(buffer)
	if err != nil {
		log.Printf("Error reading confirmation: %v", err)
		return
	}
	if string(buffer[:n]) != "TCP_CONNECTED" {
		log.Printf("Unexpected confirmation message: %s", string(buffer[:n]))
		return
	}
	log.Println("Received TCP connection confirmation from server")

	// Forward data bi-directionally
	var wg sync.WaitGroup
	wg.Add(2)

	// Upstream: SSH client -> PCP connection
	go func() {
		defer wg.Done()
		_, err := io.Copy(pcpConn, sshConn)
		if err != nil {
			log.Printf("Error forwarding SSH to PCP: %v", err)
		}
	}()

	// Downstream: PCP connection -> SSH client
	go func() {
		defer wg.Done()
		_, err := io.Copy(sshConn, pcpConn)
		if err != nil {
			log.Printf("Error forwarding PCP to SSH: %v", err)
		}
	}()

	wg.Wait()
}
