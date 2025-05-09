/*
This is a network protocol testing and verification client that works with the testserver
to validate data transmission integrity across different transport protocols (UDP, TCP,
and PCP/Pseudo-TCP).

Key Features:
1. Protocol Support:
   - UDP (User Datagram Protocol)
   - TCP (Transmission Control Protocol)
   - PCP (Pseudo-TCP, custom protocol implementation)

2. Data Verification:
   - Sends timestamped requests to server
   - Receives server responses containing timestamps and data chunks
   - Compares received data with local reference file
   - Provides colored output for verification results:
     * GREEN: Matching data
     * RED: Mismatched data with difference count
     * BLUE: Message delimiters

3. Performance Monitoring:
   - Measures transport time using timestamps
   - Supports configurable delays between packets
   - Handles MTU (Maximum Transmission Unit) settings

4. Configuration Options:
   - Server address and port
   - Source IP address
   - Protocol selection
   - MTU size
   - Maximum gap between packets
   - Reference file path

Usage:
  ./clientcompare [options]
  Options:
    -server string    Server address (default "127.0.0.1:3333")
    -file string      Path to reference file (default "book.txt")
    -mtu int         MTU size (default 1400)
    -protocol string Protocol to use (default "udp")
    -max-gap-ms int  Max delay between packets (default 1300)
    -sourceIP string Local source IP (default "127.0.0.4")

The client continuously sends requests and verifies responses until either:
- An error occurs
- Data mismatch is detected
- The program is terminated
*/

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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/filter"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rs "github.com/Clouded-Sabre/rawsocket/lib"
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
		pcpCoreObj, err = lib.NewPcpCore(pcpCoreConfig, &rscore, &filter)
		if err != nil {
			log.Println(err)
			return
		}
		defer pcpCoreObj.Close()

		// Dial to the server
		pcpConn, err = pcpCoreObj.DialPcp(sourceIpStr, serverIpStr, uint16(serverPort), connConfig)
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

		if runtime.GOOS == "windows" {
			// we assume server is always linux
			// Count the number of "\n" in serverResponse[8:]
			serverData := string(serverResponse[8:])
			newlineCount := strings.Count(serverData, "\n")

			// Read enough data from the file to match the number of "\n" in serverResponse
			fileData := make([]byte, len(serverData)+newlineCount) // Account for potential "\r\n"
			_, err = file.Read(fileData)
			if err != nil {
				fmt.Println("Error reading from file:", err)
				return
			}
			fileDataStr := strings.ReplaceAll(string(fileData), "\r\n", "\n")
			serverData = strings.ReplaceAll(serverData, "\r\n", "\n")

			// Compare the normalized data
			if serverData == fileDataStr {
				log.Printf("%sReceived: %s\n|%s%s%s|%s (Transport time: %d)\n%s", colorReset, colorBlue, colorGreen, serverData, colorBlue, colorReset, transportTime, colorReset)
			} else {
				log.Printf("%sReceived: %s\n|%s%s%s| %s(Does not match file: %s\n|%s%s%s|%s) (Transport time: %d)\n", colorReset, colorBlue, colorRed, serverData, colorBlue, colorReset, colorBlue, colorRed, fileDataStr, colorBlue, colorReset, transportTime)

				// Count and print the number of differing bytes
				countDifferences([]byte(serverData), []byte(fileDataStr))

				return // Exit the client
			}
		} else {
			fileData := make([]byte, n-8)

			_, err = file.Read(fileData)
			if err != nil {
				fmt.Println("Error reading from file:", err)
				return
			}
			if string(serverResponse[8:]) == string(fileData) {
				log.Printf("%sReceived: %s\n|%s%s%s|%s (Transport time: %d)\n%s", colorReset, colorBlue, colorGreen, serverResponse[8:], colorBlue, colorReset, transportTime, colorReset)
			} else {
				log.Printf("%sReceived: %s\n|%s%s%s| %s(Does not match file: %s\n|%s%s%s|%s) (Transport time: %d)\n", colorReset, colorBlue, colorRed, serverResponse[8:], colorBlue, colorReset, colorBlue, colorRed, fileData, colorBlue, colorReset, transportTime)

				// Count and print the number of differing bytes
				countDifferences(serverResponse[8:], fileData)

				return // Exit the client
			}
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
