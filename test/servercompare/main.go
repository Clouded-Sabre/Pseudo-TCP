package main

import (
	"flag"
	"fmt"
	"io"
	"log"
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
	colorReset = "\033[0m"
	colorGreen = "\033[32m"
	colorRed   = "\033[31m"
	colorBlue  = "\033[34m"
)

var (
	pcpCoreObj *lib.PcpCore
	filePath   string
	mtu        int
)

func main() {
	// Define command-line flags
	serverAddrFlag := flag.String("svcaddr", "0.0.0.0:8888", "Listening address in the format 'host:port'")
	filePathFlag := flag.String("file", "book.txt", "Path to the file for comparison")
	mtuFlag := flag.Int("MTU", 1400, "MTU (Maximum Transmission Unit) size")

	// Parse command-line arguments
	flag.Parse()

	// Use the flag values
	svcIpStr, serverPortStr, err := net.SplitHostPort(*serverAddrFlag)
	if err != nil {
		log.Fatal(err)
	}
	svcPort, err := strconv.Atoi(serverPortStr)
	if err != nil {
		log.Fatal(err)
	}
	filePath = *filePathFlag
	mtu = *mtuFlag

	// Create  address to listen on all available network interfaces and a specific port
	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
	}

	pcpCoreConfig := &lib.PcpCoreConfig{
		ProtocolID:      uint8(config.AppConfig.ProtocolID),
		PreferredMSS:    config.AppConfig.PreferredMSS,
		PayloadPoolSize: config.AppConfig.PayloadPoolSize,
		Debug:           config.AppConfig.Debug,
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
	svc, err := pcpCoreObj.ListenPcp(svcIpStr, svcPort, config.AppConfig)
	if err != nil {
		log.Printf("PCP server error listening at %s: %s", *serverAddrFlag, err)
		return
	}

	log.Printf("PCP service started at %s", *serverAddrFlag)

	var wg sync.WaitGroup
	// Handle Ctrl+C signal for graceful shutdown
	watcher := func(wg *sync.WaitGroup) {
		defer wg.Done()
		<-signalChan
		fmt.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(closeChan)
		fmt.Println("Closing service.")
		svc.Close() // Close the server gracefully
	}

	wg.Add(1)
	go watcher(&wg)

	for {
		// Accept incoming connections
		conn, err := svc.Accept()
		if err != nil {
			log.Println("Service stopped accepting new connection.")
			break
		} else {
			wg.Add(1)
			go handleClient(conn, &wg, closeChan)
		}
	}

	wg.Wait()
	log.Println("Server exiting...")
	pcpCoreObj.Close()
	os.Exit(0)
}

func handleClient(conn *lib.Connection, wg *sync.WaitGroup, closeChan chan struct{}) {
	defer wg.Done()

	// Open the file for comparison
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	buffer := make([]byte, mtu)

	fileLength, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Println("Error getting file length:", err)
		return
	}

	_, err = file.Seek(0, 0) // Rewind to the beginning of the file
	if err != nil {
		fmt.Println("Error rewinding file:", err)
		return
	}

	for {
		select {
		case <-closeChan:
			log.Println("Stop handleClient gracefully")
			return
		default:
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // read wait for 500 ms
			n, err := conn.Read(buffer)
			if err != nil {
				// Check if the error is a timeout
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Handle timeout error (no data received within the timeout period)
					continue // Continue waiting for incoming packets or handling closeSignal
				}
				if err == io.EOF {
					log.Println("handleClient got interuption. Stop and exit.")
					return
				}
				fmt.Println("Error reading packet:", err)
				return
			}

			message := buffer[:n]
			fileData := make([]byte, n)

			_, err = file.Read(fileData)
			if err != nil {
				fmt.Println("Error reading from file:", err)
				return
			}

			if string(message) == string(fileData) {
				log.Printf("%sReceived: %s|%s%s%s|\n%s", colorReset, colorBlue, colorGreen, message, colorBlue, colorReset)
			} else {
				log.Printf("%sReceived: %s|%s%s%s| %s(Does not match file: %s|%s%s%s|%s)\n", colorReset, colorBlue, colorRed, message, colorBlue, colorReset, colorBlue, colorRed, fileData, colorBlue, colorReset)

				// Count and print the number of differing bytes
				countDifferences(message, fileData)

				return // Exit the client
			}

			// Check if the file pointer has reached the end and rewind to the beginning if needed
			currentPos, err := file.Seek(0, io.SeekCurrent)
			if err != nil {
				fmt.Println("Error getting current file position:", err)
				return
			}

			if currentPos >= fileLength {
				_, err = file.Seek(0, 0) // Rewind to the beginning of the file
				if err != nil {
					fmt.Println("Error rewinding file:", err)
					return
				}
			}
		}

	}
}

func countDifferences(data1, data2 []byte) {
	diffCount := 0
	for i := 0; i < len(data1) && i < len(data2); i++ {
		if data1[i] != data2[i] {
			diffCount++
		}
	}
	fmt.Printf("Bytes Different: %d\n", diffCount)
}
