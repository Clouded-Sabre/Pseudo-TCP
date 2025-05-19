package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	psfilter "github.com/Clouded-Sabre/Pseudo-TCP/filter"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	rs "github.com/Clouded-Sabre/rawsocket/lib"

	"errors"
)

var (
	gatewayIP   string
	gatewayPort int
	targetAddr  string
	dropRate    float64
)

func init() {
	flag.StringVar(&gatewayIP, "ip", "127.0.0.2", "Gateway IP address")
	flag.IntVar(&gatewayPort, "port", 8901, "Gateway port number")
	flag.StringVar(&targetAddr, "target", "127.0.0.1:80", "Target server address")
	flag.Float64Var(&dropRate, "droprate", 0.1, "Packet drop rate (0.0-1.0)")
	flag.Parse()
}

// copyAndDrop reads from src and writes to dst, randomly dropping packets based on dropRate.
func copyAndDrop(dst io.Writer, src io.Reader, rate float64, rng *rand.Rand, direction string) (written int64, err error) {
	buf := make([]byte, 32*1024) // Standard buffer size for io.Copy

	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			if rng.Float64() < rate {
				log.Printf("Dropped packet in %s direction (size: %d)\n", direction, nr)
				// Packet is "dropped" by not writing it to dst
			} else {
				nw, ew := dst.Write(buf[0:nr])
				if nw < 0 || nr < nw {
					nw = 0
					if ew == nil {
						ew = errors.New("invalid write result")
					}
				}
				written += int64(nw)
				if ew != nil {
					err = ew
					break
				}
				if nr != nw {
					err = io.ErrShortWrite
					break
				}
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

func main() {
	pcpCoreConfig, connConfig, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Configuration file error: %v", err)
	}

	// Print pcpCoreConfig in pretty JSON
	pcpJSON, err := json.MarshalIndent(pcpCoreConfig, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling pcpCoreConfig to JSON: %v", err)
	}
	fmt.Println("PCP Core Configuration:")
	fmt.Println(string(pcpJSON))
	fmt.Println()

	// Print connConfig in pretty JSON
	connJSON, err := json.MarshalIndent(connConfig, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling connConfig to JSON: %v", err)
	}
	fmt.Println("Connection Configuration:")
	fmt.Println(string(connJSON))

	defaultRsConf := rs.DefaultRsConfig()
	rscore, err := rs.NewRSCore(defaultRsConf)
	if err != nil {
		log.Fatalf("Failed to create rawsocket core: %v", err)
	}
	defer rscore.Close()

	packetFilter, err := psfilter.NewFilter("PCP_DROP_GW")
	if err != nil {
		log.Fatal("Error creating filter object:", err)
	}

	pcpCoreObj, err := lib.NewPcpCore(pcpCoreConfig, &rscore, &packetFilter)
	if err != nil {
		log.Fatalf("Error creating PCP core: %v", err)
	}
	defer pcpCoreObj.Close()

	srv, err := pcpCoreObj.ListenPcp(gatewayIP, gatewayPort, connConfig)
	if err != nil {
		log.Fatalf("PCP gateway error listening at %s:%d: %v", gatewayIP, gatewayPort, err)
	}
	log.Printf("PCP gateway started at %s:%d (drop rate: %.1f%%)", gatewayIP, gatewayPort, dropRate*100)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	globalCloseChan := make(chan struct{})
	var wg sync.WaitGroup

	go func() {
		<-signalChan
		log.Println("\nReceived SIGINT (Ctrl+C). Shutting down...")
		close(globalCloseChan)
		srv.Close()
	}()

	for {
		conn, err := srv.Accept()
		if err != nil {
			select {
			case <-globalCloseChan:
				log.Println("Listener closed, server shutting down.")
			default:
				log.Printf("Error accepting PCP connection: %v", err)
			}
			break
		}
		log.Println("New PCP client connected:", conn.RemoteAddr())

		wg.Add(1)
		go handleConnection(conn, pcpCoreObj, connConfig, globalCloseChan, &wg)
	}

	wg.Wait()
	log.Println("All connections closed. Gateway exiting...")
}

func handleConnection(pcpConn *lib.Connection, pcpCoreObj *lib.PcpCore, connConfig *lib.ConnectionConfig, globalCloseChan chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	handlerDone := make(chan struct{})

	defer func() {
		log.Printf("Closing PCP connection from %s (handler exit)", pcpConn.RemoteAddr())
		pcpConn.Close()
	}()

	// Buffer client data before server connection is established
	clientToServerPipeR, clientToServerPipeW := io.Pipe()

	go func() {
		defer clientToServerPipeW.Close()
		_, err := io.Copy(clientToServerPipeW, pcpConn)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			log.Printf("Error copying from pcp client %s to pipe: %v", pcpConn.RemoteAddr(), err)
			clientToServerPipeW.CloseWithError(err)
		}
	}()

	// Parse targetAddr to get serverIP and serverPort
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		log.Printf("Invalid target address %s: %v", targetAddr, err)
		clientToServerPipeR.CloseWithError(err)
		return
	}
	serverIP := host
	serverPortUint, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		log.Printf("Invalid port in target address %s: %v", targetAddr, err)
		clientToServerPipeR.CloseWithError(err)
		return
	}
	serverPort := uint16(serverPortUint)

	// Dial PCP connection to the server
	log.Printf("Attempting to connect to server %s:%d for client %s", serverIP, serverPort, pcpConn.RemoteAddr())
	serverPcpConn, err := pcpCoreObj.DialPcp("", serverIP, serverPort, connConfig)
	if err != nil {
		log.Printf("Error connecting to server %s:%d: %v", serverIP, serverPort, err)
		clientToServerPipeR.CloseWithError(err)
		return
	}
	log.Printf("Successfully connected to server %s:%d for client %s", serverIP, serverPort, pcpConn.RemoteAddr())
	defer serverPcpConn.Close()

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var forwardWg sync.WaitGroup
	forwardWg.Add(2)

	// Forward client-to-server with packet drops
	go func() {
		defer forwardWg.Done()
		_, fwdErr := copyAndDrop(serverPcpConn, clientToServerPipeR, dropRate, rng, "client-to-server")
		if fwdErr != nil && !errors.Is(fwdErr, io.EOF) && !errors.Is(fwdErr, net.ErrClosed) && !errors.Is(fwdErr, io.ErrClosedPipe) {
			log.Printf("Error forwarding client data from %s to server %s:%d: %v", pcpConn.RemoteAddr(), serverIP, serverPort, fwdErr)
			serverPcpConn.Close()
		} else if fwdErr == nil {
			log.Printf("Client %s (via pipe) finished sending to server %s:%d", pcpConn.RemoteAddr(), serverIP, serverPort)
		}
	}()

	// Forward server-to-client with packet drops
	go func() {
		defer forwardWg.Done()
		_, fwdErr := copyAndDrop(pcpConn, serverPcpConn, dropRate, rng, "server-to-client")
		if fwdErr != nil && !errors.Is(fwdErr, io.EOF) && !errors.Is(fwdErr, net.ErrClosed) && !errors.Is(fwdErr, io.ErrClosedPipe) {
			log.Printf("Error forwarding server data from %s:%d to client %s: %v", serverIP, serverPort, pcpConn.RemoteAddr(), fwdErr)
			pcpConn.Close()
		} else if fwdErr == nil {
			log.Printf("Server %s:%d finished sending to client %s", serverIP, serverPort, pcpConn.RemoteAddr())
			pcpConn.Close()
		}
	}()

	// Wait for forwarding completion or shutdown
	go func() {
		forwardWg.Wait()
		close(handlerDone)
	}()

	select {
	case <-globalCloseChan:
		log.Printf("Global shutdown signaled, cleaning up for client %s", pcpConn.RemoteAddr())
		clientToServerPipeR.CloseWithError(io.ErrClosedPipe)
		clientToServerPipeW.CloseWithError(io.ErrClosedPipe)
	case <-handlerDone:
		// Normal completion
	}
}
