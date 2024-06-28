package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

const (
	protocol = PCP
	PCP      = 1
	UDP      = 0
)

func handleClient(conn *lib.Connection, closeChan chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	buffer := make([]byte, mtu)

	file, err := os.Open(filePath)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}

	for {
		select {
		case <-closeChan:
			log.Println("handleClient got interuption. Stop and exit.")
			return
		default:
			// Generate a random chunk size between 1 and mtu
			//chunkSize := 1 + rand.Intn(mtu-1)
			chunkSize := rand.Intn(mtu) //start from zero

			// Read data from the file
			n, err := file.Read(buffer[:chunkSize])
			if err != nil {
				log.Println("Error reading from file:", err)
				file.Close()
				break
			}

			// Send the packet to the client
			if n > 0 {
				_, err = conn.Write(buffer[:n])
			} else {
				_, err = conn.Write(nil)
			}

			if err != nil {
				log.Println("Error sending packet:", err)
				break
			}

			log.Printf("Sent packet with length %d to %s:%d\n", n, (*conn.RemoteAddr()).(*net.IPAddr).String(), conn.RemotePort())

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
}

var (
	svcAddrStr, svcIPstr, svcPortStr string
	svcPort                          int
	filePath                         string
	mtu                              int
	maxGapMs                         int
	pcpCoreObj                       *lib.PcpCore
	err                              error
)

func init() {
	// Define CLI flags for server IP and port
	flag.StringVar(&svcAddrStr, "svcaddr", "127.0.0.1:8080", "SFDP service address(IP:Port)")
	flag.StringVar(&filePath, "filepath", "book.txt", "file path to the book txt file")
	flag.IntVar(&mtu, "MTU", 1300, "payload MTU")
	flag.IntVar(&maxGapMs, "max-gap-ms", 1300, "Max gap in ms between two consecutive packets")
	flag.Parse()
}

func main() {
	svcIPstr, svcPortStr, err = net.SplitHostPort(svcAddrStr)
	if err != nil {
		log.Fatal(err)
	}
	svcPort, err = strconv.Atoi(svcPortStr)
	if err != nil {
		log.Fatal(err)
	}

	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	pcpCoreConfig := &lib.PcpCoreConfig{
		ProtocolID:      uint8(config.AppConfig.ProtocolID),
		PreferredMSS:    config.AppConfig.PreferredMSS,
		PayloadPoolSize: config.AppConfig.PayloadPoolSize,
	}

	// Create PCP server
	pcpCoreObj, err = lib.NewPcpCore(pcpCoreConfig)
	if err != nil {
		log.Println("Error creating PCP core:", err)
		return
	}
	log.Println("PCP core started.")

	// Listen for interrupt signal (Ctrl+C)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	closeChan := make(chan struct{})

	// Start the PCP server
	srv, err := pcpCoreObj.ListenPcp(svcIPstr, svcPort, config.AppConfig)
	if err != nil {
		log.Printf("PCP server error listening at %s: %s", svcAddrStr, err)
		return
	}

	log.Printf("PCP service started at %s", svcAddrStr)

	var wg sync.WaitGroup
	// Handle Ctrl+C signal for graceful shutdown
	watcher := func(wg *sync.WaitGroup) {
		defer wg.Done()
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(closeChan)
		fmt.Println("Closing service.")
		srv.Close() // Close the server gracefully
	}

	wg.Add(1)
	go watcher(&wg)

	for {
		// Accept incoming connections
		conn, err := srv.Accept()
		if err != nil {
			log.Println("Service stopped accepting new connection.")
			break
		} else {
			wg.Add(1)
			go handleClient(conn, closeChan, &wg)
		}
	}

	wg.Wait()
	log.Println("Server exiting...")
	pcpCoreObj.Close()
	os.Exit(0)
}
