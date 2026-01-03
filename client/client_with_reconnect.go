package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

// Example demonstrating how to use ReconnectingConnection
// Usage: go run client_with_reconnect.go -sourceIP <ip> -serverIP <ip> -serverPort <port>

func main() {
	// Define command-line flags
	sourceIP := flag.String("sourceIP", "127.0.0.4", "Source IP address")
	serverIP := flag.String("serverIP", "127.0.0.2", "Server IP address")
	serverPort := flag.Int("serverPort", 8901, "Server port")
	flag.Parse()

	const (
		iteration         = 1000
		numOfPackets      = 20
		msOfSleep         = 1000
		iterationInterval = 15 // in seconds
	)

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

	buffer := make([]byte, config.AppConfig.PreferredMSS)
	for j := 0; j < iteration; j++ {
		// Dial to the server
		baseConn, err := pcpCoreObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort), config.AppConfig)
		if err != nil {
			fmt.Println("Error connecting:", err)
			return
		}

		// Wrap the connection with automatic reconnection capability
		reconnectConfig := &lib.ReconnectConfig{
			Enabled:           true,
			MaxRetries:        5,                    // Try up to 5 times
			InitialBackoff:    500 * time.Millisecond, // Start with 500ms
			MaxBackoff:        10 * time.Second,     // Cap at 10 seconds
			BackoffMultiplier: 1.5,                  // 1.5x exponential backoff
			OnReconnect: func() {
				log.Println("Successfully reconnected to server!")
			},
			OnFinalFailure: func() {
				log.Println("Failed to reconnect after all attempts. Giving up.")
			},
		}

		// Create the reconnecting connection wrapper
		dialCfg := &lib.DialConfig{
			PcpCore:    pcpCoreObj,
			LocalIP:    *sourceIP,
			ServerIP:   *serverIP,
			ServerPort: uint16(*serverPort),
			PcpConfig:  config.AppConfig,
		}
		conn := lib.NewReconnectingConnection(baseConn, reconnectConfig, dialCfg)

		fmt.Println("PCP connection established with auto-reconnect enabled!")

		// Simulate data transmission
		for i := 0; i < numOfPackets; i++ {
			// Construct a packet
			payload := []byte(fmt.Sprintf("Data packet %d \n", i))

			// Send the packet to the server
			fmt.Println("Sending packet", i)
			_, err := conn.Write(payload)
			if err != nil {
				fmt.Println("Error writing:", err)
				break
			}
			log.Printf("Packet %d sent.\n", i)

			lib.SleepForMs(msOfSleep) // Simulate some delay between packets
		}

		payload := []byte("Client Done")
		_, err = conn.Write(payload)
		if err != nil {
			fmt.Println("Error writing final packet:", err)
		} else {
			log.Println("Final packet sent:", string(payload))
		}

		for {
			n, err := conn.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Connection closed by the server, exit the loop
					fmt.Println("Server closed the connection.")
					break
				} else {
					fmt.Println("Error reading:", err)
					continue
				}
			}
			log.Println("Received packet:", string(buffer[:n]))
		}

		// Close the connection
		conn.Close()
		time.Sleep(time.Second * iterationInterval)
	}
	fmt.Println("PCP client with reconnect exit")
}

// sleep for n milliseconds
func SleepForMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
