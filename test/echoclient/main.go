package main

import (
	"flag"
	"fmt"
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
	packetInterval := flag.Duration("interval", 500*time.Millisecond, "Interval between packets (e.g., 500ms, 1s)")
	flag.Parse()

	var err error
	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configuration file error:", err)
	}

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

	// Create reconnection helper with default configuration
	reconnectCfg := lib.DefaultClientReconnectConfig()
	reconnectCfg.OnReconnect = func() {
		log.Println("[RECONNECT] Successfully reconnected to echo server")
	}
	reconnectCfg.OnFinalFailure = func(err error) {
		log.Printf("[RECONNECT] Failed to reconnect after all retries: %v\n", err)
	}

	reconnectHelper := lib.NewClientReconnectHelper(
		pcpCoreObj,
		*sourceIP,
		*serverIP,
		uint16(*serverPort),
		config.AppConfig,
		reconnectCfg,
	)

	buffer := make([]byte, config.AppConfig.PreferredMSS)

	// Dial to the server
	conn, err := pcpCoreObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort), config.AppConfig)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}

	// Set the initial connection in the helper
	reconnectHelper.SetConnection(conn)
	fmt.Println("Echo client connected to server!")
	fmt.Printf("Sending packets at %v interval (press Ctrl+C to exit)...\n", *packetInterval)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Send and receive echo messages
	successCount := 0
	failureCount := 0
	packetCount := 0

	// Create ticker for sending packets at specified interval
	ticker := time.NewTicker(*packetInterval)
	defer ticker.Stop()

	// Create a done channel for clean shutdown on signal
	done := make(chan struct{})
	go func() {
		<-sigChan
		close(done)
	}()

	for {
		select {
		case <-done:
			// User pressed Ctrl+C
			goto shutdown
		case <-ticker.C:
			// Send packet at specified interval
			packetCount++
			message := fmt.Sprintf("Echo message %d", packetCount)
			payload := []byte(message)

			// Send the message to the server
			log.Printf("[%d] Sending: %s\n", packetCount, message)
			currentConn := reconnectHelper.GetConnection()
			n, err := currentConn.Write(payload)

			if err != nil {
				if reconnectHelper.HandleError(err) {
					// Reconnected successfully, retry the write
					log.Printf("[%d] Reconnected, retrying write\n", packetCount)
					currentConn = reconnectHelper.GetConnection()
					n, err = currentConn.Write(payload)
					if err != nil {
						log.Printf("[%d] Error writing after reconnect: %v\n", packetCount, err)
						failureCount++
						continue
					}
				} else {
					log.Printf("[%d] Error writing and reconnection failed: %v\n", packetCount, err)
					failureCount++
					continue
				}
			}
			log.Printf("[%d] Sent %d bytes\n", packetCount, n)

			// Read echo response from the server with timeout for responsiveness to signals
			currentConn = reconnectHelper.GetConnection()
			// Set read deadline slightly after next ticker to allow receiving responses while being responsive to Ctrl+C
			currentConn.SetReadDeadline(time.Now().Add(*packetInterval + 100*time.Millisecond))
			n, err = currentConn.Read(buffer)

			if err != nil {
				// Check for read deadline timeout (just skip this iteration, server may not have responded yet)
				if err.Error() == "Read deadline exceeded" {
					log.Printf("[%d] Read timeout (no response yet), continuing...\n", packetCount)
					continue
				}
				if _, isKeepaliveTimeout := err.(*lib.KeepAliveTimeoutError); isKeepaliveTimeout {
					// Keepalive timeout, attempt reconnection
					log.Printf("[%d] Keepalive timeout detected, attempting reconnection\n", packetCount)
					if reconnectHelper.HandleError(err) {
						log.Printf("[%d] Reconnected, retrying read\n", packetCount)
						// Retry read after reconnection
						currentConn = reconnectHelper.GetConnection()
						currentConn.SetReadDeadline(time.Now().Add(*packetInterval + 100*time.Millisecond))
						n, err = currentConn.Read(buffer)
						if err != nil {
							log.Printf("[%d] Error reading after reconnect: %v\n", packetCount, err)
							failureCount++
							continue
						}
					} else {
						log.Printf("[%d] Reconnection failed\n", packetCount)
						failureCount++
						goto shutdown
					}
				} else if err == io.EOF {
					// Server closed the connection gracefully
					log.Println("Server closed the connection.")
					failureCount++
					goto shutdown
				} else {
					log.Printf("[%d] Error reading: %v\n", packetCount, err)
					failureCount++
					continue
				}
			}

			response := string(buffer[:n])
			log.Printf("[%d] Received: %s\n", packetCount, response)

			if response == message {
				log.Printf("[%d] ✓ Echo match!\n", packetCount)
				successCount++
			} else {
				log.Printf("[%d] ✗ Echo mismatch! Expected: %s, Got: %s\n", packetCount, message, response)
				failureCount++
			}
		}
	}

shutdown:
	// Print statistics
	fmt.Printf("\n=== Echo Client Statistics ===\n")
	fmt.Printf("Total packets sent: %d\n", packetCount)
	fmt.Printf("Successful echoes: %d\n", successCount)
	fmt.Printf("Failed echoes: %d\n", failureCount)
	if packetCount > 0 {
		successRate := float64(successCount) / float64(packetCount) * 100
		fmt.Printf("Success rate: %.1f%%\n", successRate)
	}

	// Close connection
	currentConn := reconnectHelper.GetConnection()
	if currentConn != nil {
		currentConn.Close()
	}

	fmt.Println("Echo client exit")
}
