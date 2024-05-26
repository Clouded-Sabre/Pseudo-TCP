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
		log.Fatalln("Configurtion file error:", err)
	}

	connConfig := &lib.ConnectionConfig{
		WindowScale:             config.AppConfig.WindowScale,
		PreferredMSS:            config.AppConfig.PreferredMSS,
		SackPermitSupport:       config.AppConfig.SackPermitSupport,
		SackOptionSupport:       config.AppConfig.SackOptionSupport,
		IdleTimeout:             config.AppConfig.IdleTimeout,
		KeepAliveEnabled:        config.AppConfig.KeepAliveEnabled,
		KeepaliveInterval:       config.AppConfig.KeepaliveInterval,
		MaxKeepaliveAttempts:    config.AppConfig.MaxKeepaliveAttempts,
		ResendInterval:          config.AppConfig.ResendInterval,
		MaxResendCount:          config.AppConfig.MaxResendCount,
		Debug:                   true,
		WindowSizeWithScale:     config.AppConfig.WindowSizeWithScale,
		ConnSignalRetryInterval: config.AppConfig.ConnSignalRetryInterval,
		ConnSignalRetry:         config.AppConfig.ConnSignalRetry,
	}
	pcpConfig := &lib.PcpProtocolConnConfig{
		IptableRuleDaley:     config.AppConfig.IptableRuleDaley,
		PreferredMSS:         config.AppConfig.PreferredMSS,
		PacketLostSimulation: config.AppConfig.PacketLostSimulation,
		PConnTimeout:         config.AppConfig.PConnTimeout,
		ClientPortUpper:      config.AppConfig.ClientPortUpper,
		ClientPortLower:      config.AppConfig.ClientPortLower,
		ConnConfig:           connConfig,
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
		conn, err := pcpCoreObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort), pcpConfig)
		if err != nil {
			fmt.Println("Error connecting:", err)
			return
		}
		//defer conn.Close()

		fmt.Println("PCP connection established!")

		// Simulate data transmission
		for i := 0; i < numOfPackets; i++ {
			// Construct a packet
			payload := []byte(fmt.Sprintf("Data packet %d \n", i))

			// Send the packet to the server
			fmt.Println("Sending packet", i)
			conn.Write(payload)
			log.Printf("Packet %d sent.\n", i)

			SleepForMs(msOfSleep) // Simulate some delay between packets
		}

		payload := []byte("Client Done")
		conn.Write(payload)
		log.Println("Packet sent:", string(payload))

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
			/*if string(buffer[:n]) == "Server Done" {
				break
			}*/
		}

		// Close the connection
		//conn.Close()
		time.Sleep(time.Second * iterationInterval)
	}
	fmt.Println("PCP client exit")
}

// sleep for n milliseconds
func SleepForMs(n int) {
	timeout := time.After(time.Duration(n) * time.Millisecond)
	<-timeout // Wait on the channel
}
