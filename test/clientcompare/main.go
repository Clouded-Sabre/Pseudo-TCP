package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	//"time"
)

func main() {
	// Define command-line flags
	serverAddrFlag := flag.String("server", "127.0.0.1:3333", "Server address in the format 'host:port'")
	filePathFlag := flag.String("file", "book.txt", "Path to the file for comparison")
	sourceIpStr := flag.String("sourceIP", "127.0.0.4", "local source IP address")
	mtuFlag := flag.Int("mtu", 1400, "MTU (Maximum Transmission Unit) size")

	// Parse command-line arguments
	flag.Parse()

	// Check the number of arguments
	if flag.NArg() != 0 {
		fmt.Println("Usage: your-program [options]")
		flag.PrintDefaults()
		return
	}

	// Use the flag values
	serverIpStr, serverPortStr, err := net.SplitHostPort(*serverAddrFlag)
	if err != nil {
		log.Fatal(err)
	}
	serverPort, err := strconv.Atoi(serverPortStr)
	if err != nil {
		log.Fatal(err)
	}
	filePath := *filePathFlag
	mtu := *mtuFlag

	colorReset := "\033[0m"
	colorGreen := "\033[32m"
	colorRed := "\033[31m"
	colorBlue := "\033[34m"

	config.AppConfig, err = config.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalln("Configurtion file error:", err)
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

	// Dial to the server
	conn, err := pcpCoreObj.DialPcp(*sourceIpStr, serverIpStr, uint16(serverPort), config.AppConfig)
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}
	//defer conn.Close()

	fmt.Println("PCP connection established!")

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

	// Send "Hi" to the server
	hiMessage := []byte("Hi")
	_, err = conn.Write(hiMessage)
	if err != nil {
		fmt.Println("Error sending 'Hi' to server:", err)
		return
	}

	// Read and compare responses from the server
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Error reading from PCP connection:", err)
			return
		}

		serverResponse := buffer[:n]
		fileData := make([]byte, n)

		_, err = file.Read(fileData)
		if err != nil {
			fmt.Println("Error reading from file:", err)
			return
		}

		if string(serverResponse) == string(fileData) {
			log.Printf("%sReceived: %s|%s%s%s|\n%s", colorReset, colorBlue, colorGreen, serverResponse, colorBlue, colorReset)
		} else {
			log.Printf("%sReceived: %s|%s%s%s| %s(Does not match file: %s|%s%s%s|%s)\n", colorReset, colorBlue, colorRed, serverResponse, colorBlue, colorReset, colorBlue, colorRed, fileData, colorBlue, colorReset)

			// Count and print the number of differing bytes
			countDifferences(serverResponse, fileData)

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
