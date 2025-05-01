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

	// Create PCP core
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig, &rscore, "PCP_anchor")
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

	// Find appropriate local IP for PCP connection
	localIP, err := findLocalIP(serverIP)
	if err != nil {
		log.Printf("Error finding local IP: %v", err)
		return
	}
	log.Printf("Selected local IP: %s", localIP)

	// Dial PCP connection to forwarder server
	pcpConn, err := pcpCoreObj.DialPcp(localIP, serverIP, uint16(serverPort), connConfig)
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

// findLocalIP selects a local IP address that is in the same subnet as the targetIP,
// or falls back to an IP in the same subnet as the default gateway.
func findLocalIP(targetIP string) (string, error) {
	// Parse target IP
	target := net.ParseIP(targetIP)
	if target == nil {
		return "", fmt.Errorf("invalid target IP: %s", targetIP)
	}

	// Assume a /24 subnet mask for simplicity
	targetNet := &net.IPNet{
		IP:   target,
		Mask: net.CIDRMask(24, 32), // 255.255.255.0
	}

	// Get all network interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %v", err)
	}

	// Check for an interface in the same subnet as targetIP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue // Skip down or loopback interfaces
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() == nil {
				continue // Skip IPv6
			}

			// Check if local IP is in the same subnet as targetIP
			localNet := &net.IPNet{
				IP:   ip,
				Mask: net.CIDRMask(24, 32),
			}
			if localNet.Contains(target) || targetNet.Contains(ip) {
				return ip.String(), nil
			}
		}
	}

	// Fallback: Return the first non-loopback IPv4 address
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable local IP found for target %s", targetIP)
}
