package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"strconv"
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

func main() {
	// Define command-line flags
	serverAddrFlag := flag.String("server", "127.0.0.1:3333", "Server address in the format 'host:port'")
	filePathFlag := flag.String("file", "book.txt", "Path to the file for comparison")
	mtuFlag := flag.Int("mtu", 1400, "MTU (Maximum Transmission Unit) size")
	protocolFlag := flag.String("protocol", "udp", "Transport protocol: udp, tcp, and pcp")
	maxGapMsFlag := flag.Int("max-gap-ms", 1300, "Max gap in ms between two consecutive packets")
	sourceIpFlag := flag.String("sourceIP", "127.0.0.4", "local source IP address")

	// Parse command-line arguments
	flag.Parse()

	// Check the number of arguments
	if flag.NArg() != 0 {
		fmt.Println("Usage: your-program [options]")
		flag.PrintDefaults()
		return
	}

	// Use the flag values
	serverAddr := *serverAddrFlag
	filePath := *filePathFlag
	mtu := *mtuFlag
	protocol := *protocolFlag
	maxGapMs := *maxGapMsFlag
	sourceIpStr := *sourceIpFlag

	var (
		conn       net.Conn
		pcpCoreObj *lib.PcpCore
		pcpConn    *lib.Connection
		err        error
	)

	switch protocol {
	case "udp":
		// Create a UDP connection to the server
		serverAddr, err := net.ResolveUDPAddr("udp", serverAddr)
		if err != nil {
			fmt.Println("Error resolving server address:", err)
			return
		}

		conn, err = net.DialUDP("udp", nil, serverAddr)
		if err != nil {
			fmt.Println("Error creating UDP connection:", err)
			return
		}
		defer conn.Close()
		fmt.Println("UDP client is connected to", serverAddr)

	case "tcp":
		// Create a TCP connection to the server
		conn, err = net.Dial("tcp", serverAddr)
		if err != nil {
			fmt.Println("Error creating TCP connection:", err)
			return
		}
		defer conn.Close()
		fmt.Println("TCP client is connected to", serverAddr)
	case "pcp":
		// Create a TCP connection to the server
		// Use the flag values
		serverIpStr, serverPortStr, err := net.SplitHostPort(*serverAddrFlag)
		if err != nil {
			log.Fatal(err)
		}
		serverPort, err := strconv.Atoi(serverPortStr)
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
		pcpCoreObj, err = lib.NewPcpCore(pcpCoreConfig)
		if err != nil {
			log.Println(err)
			return
		}
		defer pcpCoreObj.Close()

		// Dial to the server
		pcpConn, err = pcpCoreObj.DialPcp(sourceIpStr, serverIpStr, uint16(serverPort), config.AppConfig)
		if err != nil {
			fmt.Println("Error connecting:", err)
			return
		}
		defer pcpConn.Close()

		fmt.Println("PCP connection established!")
	default:
		fmt.Println("Invalid protocol specified. Use 'udp' or 'tcp'.")
		return
	}

	if protocol == "pcp" {
		go handleResponse(nil, pcpConn, filePath, mtu)
	} else {
		go handleResponse(&conn, nil, filePath, mtu)
	}

	// Sleep for 200 milliseconds to make sure handleResponse is ready
	sleepDuration := time.Duration(200) * time.Millisecond
	time.Sleep(sleepDuration)

	if protocol == "pcp" {
		go handleRequest(nil, pcpConn, maxGapMs)
	} else {
		go handleRequest(&conn, nil, maxGapMs)
	}

	select {}

}

func handleRequest(conn *net.Conn, pcpConn *lib.Connection, maxGapMs int) {
	var err error
	for {
		reqMessage := appendTimestamp(nil)
		if conn != nil {
			_, err = (*conn).Write(reqMessage)
		} else {
			_, err = pcpConn.Write(reqMessage)
		}

		if err != nil {
			fmt.Println("Error sending request to server:", err)
			return
		}

		// Sleep for a random duration between 0 and maxGapMs milliseconds
		sleepDuration := time.Duration(rand.Intn(maxGapMs)) * time.Millisecond
		time.Sleep(sleepDuration)
	}

}

func handleResponse(conn *net.Conn, pcpConn *lib.Connection, filePath string, mtu int) {
	// Open the local file for comparison
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

	var (
		n int
	)
	// Read and compare responses from the server
	for {
		if conn != nil {
			n, err = (*conn).Read(buffer)
		} else {
			n, err = pcpConn.Read(buffer)
		}

		if err != nil {
			fmt.Println("Error reading from connection:", err)
			return
		}

		serverResponse := buffer[:n]
		// Extract timestamp and calculate time since it
		messageTime, err := DecodeTime(buffer[:8])
		if err != nil {
			log.Fatal("Timestamp decoding error:", err)
		}
		transportTime := time.Since(messageTime).Milliseconds()
		//timestamp := int64(binary.BigEndian.Uint64(serverResponse[:8]))
		//transportTime := time.Since(time.Unix(timestamp, 0)).Milliseconds()

		fileData := make([]byte, n-8)

		_, err = file.Read(fileData)
		if err != nil {
			fmt.Println("Error reading from file:", err)
			return
		}

		if string(serverResponse[8:]) == string(fileData) {
			log.Printf("%sReceived: %s|%s%s%s|%s (Transport time: %d)\n%s", colorReset, colorBlue, colorGreen, serverResponse[8:], colorBlue, colorReset, transportTime, colorReset)
		} else {
			log.Printf("%sReceived: %s|%s%s%s| %s(Does not match file: %s|%s%s%s|%s) (Transport time: %d)\n", colorReset, colorBlue, colorRed, serverResponse[8:], colorBlue, colorReset, colorBlue, colorRed, fileData, colorBlue, colorReset, transportTime)

			// Count and print the number of differing bytes
			countDifferences(serverResponse[8:], fileData)

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

func appendTimestamp(data []byte) []byte {
	timestampBytes := EncodeTime(time.Now())
	return append(timestampBytes, data...)
}

func countDifferences(data1, data2 []byte) {
	diffCount := 0
	for i := 0; i < len(data1) && i < len(data2); i++ {
		if data1[i] != data2[i] {
			fmt.Print(i)
			fmt.Print(" ")
			diffCount++
		}
	}
	fmt.Printf("\nBytes Different: %d\n", diffCount)
}

func EncodeTime(t time.Time) []byte {
	nanoseconds := t.UnixNano()
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, uint64(nanoseconds))
	return data
}

func DecodeTime(data []byte) (time.Time, error) {
	if len(data) != 8 {
		return time.Time{}, fmt.Errorf("invalid data size, expected 8 bytes")
	}
	nanoseconds := int64(binary.BigEndian.Uint64(data))
	return time.Unix(0, nanoseconds), nil
}
