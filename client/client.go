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

	pcpClientObj := client.NewPcpClient(uint8(config.AppConfig.ProtocolID))
	// Dial to the server
	conn, err := pcpClientObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort))
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	defer conn.Close()

	fmt.Println("PCP connection established!")

	// Simulate data transmission
	for i := 0; i < 10; i++ {
		// Construct a packet
		payload := []byte(fmt.Sprintf("Data packet %d \n", i))

		// Send the packet to the server
		fmt.Println("Sending packet", i)
		conn.Write(payload)
		log.Printf("Packet %d sent.\n", i)

		time.Sleep(time.Second) // Simulate some delay between packets
	}

	payload := []byte("Client Done")
	conn.Write(payload)
	log.Println("Packet sent:", string(payload))

	buffer := make([]byte, config.AppConfig.PreferredMSS)
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
	time.Sleep(time.Second * 3)
	fmt.Println("PCP client exit")
}
