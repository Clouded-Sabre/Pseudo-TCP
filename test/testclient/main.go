/*
This is a test client designed to work with servercompare for protocol testing and
data integrity verification. The client sends random-sized chunks of a file using
the PCP (Pseudo-TCP) protocol with configurable timing delays between transmissions.

Key Features:
1. Data Transmission:
   - Reads data from a source file in random-sized chunks
   - Sends chunks to servercompare for verification
   - Supports configurable MTU (Maximum Transmission Unit)
   - Automatically rotates through file content when reaching EOF

2. PCP Protocol Support:
   - Implements custom Pseudo-TCP protocol
   - Configurable through YAML file
   - Handles connection establishment and management
   - Supports custom source IP addressing

3. Traffic Control:
   - Random delays between packet transmissions
   - Configurable maximum gap between packets
   - Variable chunk sizes (1 to MTU)
   - Continuous operation with file rotation

4. Configuration Options:
   - Server address (default: 127.0.0.1:8080)
   - Source IP address (default: 127.0.0.4)
   - MTU size (default: 1000)
   - Maximum gap between packets (default: 1300ms)
   - Source file path (default: book.txt)
   - PCP protocol settings via config.yaml

Usage:
  ./testclient [options]
  Options:
    -serveraddr string Server address (default "127.0.0.1:8080")
    -sourceIP string   Source IP address (default "127.0.0.4")
    -file string      Source file path (default "book.txt")
    -MTU int         MTU size (default 1000)
    -max-gap-ms int  Max delay between packets (default 1300)

The client operates by:
1. Establishing PCP connection with servercompare
2. Reading random-sized chunks from source file
3. Sending chunks with random delays between transmissions
4. Rotating through file content when reaching EOF
5. Continuing transmission until interrupted

This client is designed to work with servercompare for testing protocol
reliability and data transmission integrity.
*/

package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/filter"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rs "github.com/Clouded-Sabre/rawsocket/lib"
)

var (
	serverAddrStr, sourceIpStr string
	filePath                   string
	mtu                        int
	maxGapMs                   int
)

func init() {
	// Define CLI flags for server IP and port
	flag.StringVar(&serverAddrStr, "serveraddr", "127.0.0.1:8080", "SFDP service address(IP:Port)")
	flag.StringVar(&filePath, "file", "book.txt", "file path to the book txt file")
	flag.IntVar(&mtu, "MTU", 1000, "payload MTU")
	flag.StringVar(&sourceIpStr, "sourceIP", "127.0.0.4", "local source IP address")
	flag.IntVar(&maxGapMs, "max-gap-ms", 1300, "Max gap in ms between two consecutive packets")
	flag.Parse()
}

func main() {
	// Use the flag values
	serverIpStr, serverPortStr, err := net.SplitHostPort(serverAddrStr)
	if err != nil {
		log.Fatal(err)
	}
	serverPort, err := strconv.Atoi(serverPortStr)
	if err != nil {
		log.Fatal(err)
	}

	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file:", err)
		os.Exit(1)
	}
	defer file.Close()

	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	defaultRsConf := rs.DefaultRsConfig()
	rscore, err := rs.NewRSCore(defaultRsConf)
	if err != nil {
		log.Fatal("Failed to create rawsocket core. exit!")
	}
	defer rscore.Close()

	filter, err := filter.NewFilter("PCP_anchor")
	if err != nil {
		log.Fatal("Error creating filter object:", err)
	}
	// Create PCP server
	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig, &rscore, &filter)
	if err != nil {
		log.Println(err)
		return
	}
	defer pcpCoreObj.Close()

	// Dial to the server
	conn, err := pcpCoreObj.DialPcp(sourceIpStr, serverIpStr, uint16(serverPort), connConfig)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	//defer conn.Close()

	fmt.Println("PCP connection established!")

	buffer := make([]byte, mtu)
	//rand.Seed(time.Now().UnixNano())

	for {
		// Generate a random chunk size between 1 and mtu
		//chunkSize := 1 + rand.Intn(mtu - 1)
		chunkSize := rand.Intn(mtu) // start from zero

		// Read data from the file
		n, err := file.Read(buffer[:chunkSize])
		if err != nil {
			fmt.Println("Error reading from file:", err)
			break
		}

		// Send the packet to the server
		if n > 0 {
			_, err = conn.Write(buffer[:n])
		} else {
			_, err = conn.Write(nil)
		}

		if err != nil {
			fmt.Println("Error sending packet:", err)
			break
		}

		log.Printf("Sent packet with length %d\n", n)

		// Sleep for a random duration between 0 and maxGapMs milliseconds
		sleepDuration := time.Duration(rand.Intn(maxGapMs)) * time.Millisecond
		time.Sleep(sleepDuration)

		// Check if we have reached the end of the file
		if n < chunkSize {
			// Reset the read pointer to the beginning of the file
			_, err = file.Seek(0, 0)
			if err != nil {
				log.Println("Error seeking to the beginning of the file:", err)
				return
			}
			//break
		}
	}
}
