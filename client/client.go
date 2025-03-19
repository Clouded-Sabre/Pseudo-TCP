/*
This is a Pseudo-TCP (PCP) client implementation that performs iterative connection
testing with a PCP server. The client establishes connections and exchanges test
packets to validate protocol reliability and performance.

Key Features:
1. Connection Management:
   - Establishes PCP connections to server
   - Performs 1000 test iterations
   - Implements configurable delays between iterations
   - Handles connection cleanup and resource management

2. Data Exchange Protocol:
   - Sends 20 sequential test packets per iteration
   - Adds 1-second delay between packets
   - Sends "Client Done" message after packets
   - Reads server responses until connection closure
   - Waits 15 seconds between iterations

3. Error Handling:
   - Connection failures
   - Read/Write errors
   - EOF detection
   - Resource cleanup
   - Graceful iteration management

4. Configuration Options:
   - Source IP address (default: 127.0.0.4)
   - Server IP address (default: 127.0.0.2)
   - Server port (default: 8901)
   - PCP protocol settings via config.yaml
   - Configurable packet delay (default: 1000ms)

Usage:
  ./client [options]
  Options:
    -sourceIP string  Source IP address (default "127.0.0.4")
    -serverIP string  Server IP address (default "127.0.0.2")
    -serverPort int   Server port number (default 8901)

The client operates by:
1. Loading configuration from config.yaml
2. Creating PCP core instance
3. Running 1000 iterations of:
   - Establishing connection to server
   - Sending 20 test packets with delays
   - Sending "Client Done" message
   - Reading server responses
   - Waiting 15 seconds before next iteration
4. Cleaning up resources on completion

This client implements the PCP protocol for testing network connection
reliability and transmission characteristics.
*/

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

	//var err error
	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println(err)
		return
	}
	defer pcpCoreObj.Close()

	buffer := make([]byte, pcpCoreConfig.PreferredMSS)
	for j := 0; j < iteration; j++ {
		// Dial to the server
		pcpCoreConfig.PcpProtocolConnConfig.ConnConfig = connConfig
		conn, err := pcpCoreObj.DialPcp(*sourceIP, *serverIP, uint16(*serverPort), connConfig)
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
