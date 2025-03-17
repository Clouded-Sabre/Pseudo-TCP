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
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
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

	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig)
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
