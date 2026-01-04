package main

import (
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

func main() {
	// Define command-line flags
	sourceIP := flag.String("sourceIP", "127.0.0.4", "Source IP address")
	serverIP := flag.String("serverIP", "127.0.0.2", "Server IP address")
	serverPort := flag.Int("serverPort", 8901, "Server port")
	packetInterval := flag.Duration("interval", 500*time.Millisecond, "Time between packets")
	flag.Parse()

	// Load configuration
	var err error
	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configuration file error:", err)
	}

	// Initialize PCP core
	pcpCoreConfig := &lib.PcpCoreConfig{
		ProtocolID:      uint8(config.AppConfig.ProtocolID),
		PreferredMSS:    config.AppConfig.PreferredMSS,
		PayloadPoolSize: config.AppConfig.PayloadPoolSize,
	}
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println(err)
		return
	}
	defer pcpCoreObj.Close()

	// Create reconnection helper with configuration
	reconnectCfg := &lib.ClientReconnectConfig{
		MaxRetries:        10,
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        60 * time.Second,
		BackoffMultiplier: 1.5,
		OnReconnect: func() {
			log.Println("✅ Successfully reconnected to server")
		},
		OnFinalFailure: func(err error) {
			log.Printf("❌ Failed to reconnect after all attempts: %v\n", err)
		},
	}

	helper := lib.NewClientReconnectHelper(
		pcpCoreObj,
		*sourceIP,
		*serverIP,
		uint16(*serverPort),
		config.AppConfig,
		reconnectCfg,
	)

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		<-sigChan
		close(done)
	}()

	// Create initial connection
	conn, err := pcpCoreObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort), config.AppConfig)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	log.Printf("Connected to %s\n", *serverIP)

	// Set initial connection in helper
	helper.SetConnection(conn)

	// Ticker for sending packets
	ticker := time.NewTicker(*packetInterval)
	defer ticker.Stop()

	packetCount := 0
	buffer := make([]byte, config.AppConfig.PreferredMSS)

	// Main event loop
	for {
		select {
		case <-done:
			log.Println("Shutting down...")
			goto shutdown

		case <-ticker.C:
			packetCount++

			// Prepare payload
			payload := []byte("PCP client packet")

			// Try to send
			currentConn := helper.GetConnection()
			n, err := currentConn.Write(payload)
			if err != nil {
				log.Printf("[%d] ERROR: Write error - %v\n", packetCount, err)

				// Check if it's a keepalive timeout (needs reconnection)
				if isKeepAliveTimeoutError(err) {
					log.Printf("[%d] ERROR: Keepalive timeout detected, initiating reconnection\n",
						packetCount)

					// Attempt reconnection (HandleError manages backoff internally)
					if !helper.HandleError(err) {
						log.Printf("[%d] ERROR: Reconnection failed\n", packetCount)
						continue
					}

					// Success! Get the new connection
					currentConn = helper.GetConnection()
					log.Printf("[%d] ✅ Reconnection successful\n", packetCount)
				}
				continue
			}

			// Set read deadline for responsiveness to Ctrl+C
			currentConn.SetReadDeadline(time.Now().Add(*packetInterval + 100*time.Millisecond))
			n, err = currentConn.Read(buffer)

			if err != nil {
				// Check for read deadline timeout (normal, not an error)
				if err.Error() == "Read deadline exceeded" {
					continue
				}

				log.Printf("[%d] ERROR: Read error - %v\n", packetCount, err)

				if isKeepAliveTimeoutError(err) {
					log.Printf("[%d] ERROR: Keepalive timeout detected, initiating reconnection\n",
						packetCount)

					if !helper.HandleError(err) {
						log.Printf("[%d] ERROR: Reconnection failed\n", packetCount)
						continue
					}

					currentConn = helper.GetConnection()
					log.Printf("[%d] ✅ Reconnection successful\n", packetCount)
				} else if err == io.EOF {
					log.Println("Server closed the connection")
				}
				continue
			}

			log.Printf("[%d] Sent: %d bytes, Received: %d bytes\n",
				packetCount, n, len(buffer[:n]))
		}
	}

shutdown:
	currentConn := helper.GetConnection()
	if currentConn != nil {
		currentConn.Close()
	}
	log.Println("Client closed")
}

// isKeepAliveTimeoutError checks if an error is a keepalive timeout
func isKeepAliveTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*lib.KeepAliveTimeoutError)
	return ok
}
