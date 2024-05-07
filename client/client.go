package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib/client"
)

func main() {
	// load config
	config.AppConfig, _ = config.ReadConfig()

	// Define command-line flags
	sourceIP := flag.String("sourceIP", config.AppConfig.ClientIP, "Source IP address")
	serverIP := flag.String("serverIP", config.AppConfig.ServerIP, "Server IP address")
	serverPort := flag.Int("serverPort", config.AppConfig.ServerPort, "Server port")
	flag.Parse()

	const (
		iteration         = 1000
		numOfPackets      = 20
		msOfSleep         = 1000
		iterationInterval = 15 // in seconds
	)

	pcpClientObj := client.NewPcpClient(uint8(config.AppConfig.ProtocolID))
	defer pcpClientObj.Close()

	buffer := make([]byte, config.AppConfig.PreferredMSS)
	for j := 0; j < iteration; j++ {
		// Dial to the server
		conn, err := pcpClientObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort))
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
				fmt.Println("Error reading:", err)
				continue
			}
			log.Println("Received packet:", string(buffer[:n]))
			if string(buffer[:n]) == "Server Done" {
				break
			}
		}

		// Close the connection
		conn.Close()
		time.Sleep(time.Second * iterationInterval)
	}
	fmt.Println("PCP client exit")
}

// sleep for n milliseconds
func SleepForMs(n int) {
	timeout := time.After(time.Duration(n) * time.Millisecond)
	<-timeout // Wait on the channel
}
