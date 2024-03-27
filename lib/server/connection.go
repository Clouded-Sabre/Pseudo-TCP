package server

import (
	"fmt"
	"log"
	"math"
	"net"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type Connection struct {
	attrs           *lib.Connection // common connection attributes
	OpenServerState uint            // 3-way handshake server states
	pcpService      *Service
}

var Mu sync.Mutex

func newConnection(key string, srv *Service, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int) (*Connection, error) {
	isn, _ := lib.GenerateISN()
	options := &lib.Options{
		WindowScaleShiftCount: config.WindowScale,
		MSS:                   config.PreferredMss,
		SupportSack:           true,
	}
	newAttrs := &lib.Connection{
		Key:                key,
		RemoteAddr:         remoteAddr,
		RemotePort:         remotePort,
		LocalAddr:          localAddr,
		LocalPort:          localPort,
		NextSequenceNumber: isn,
		LastAckNumber:      0,
		WindowSize:         math.MaxUint16,
		InputChannel:       make(chan *lib.PcpPacket),
		ReadChannel:        make(chan []byte),
		OutputChan:         srv.OutputChan,
		// all the rest variables keep there init value
		TcpOptions: options,
	}

	newClientConn := &Connection{
		attrs:           newAttrs,
		pcpService:      srv,
		OpenServerState: 0,
	}

	return newClientConn, nil
}

func (c *Connection) handleIncomingPackets() {
	// Handle client requests - main loop of the connection for handling incoming packets
	// Only cares about data packets and 4-way termination packets
	// if it is a data packet, acknowledge it and any packet which is missing since last acknowledged packet. Then put it into readChannel to be read by Connection.read function
	// if it is a 4-way termination packets call RespHandleCloseConnection
	var (
		packet                     *lib.PcpPacket
		isACK, isFIN, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		packet = <-c.attrs.InputChannel
		// Extract SYN and ACK flags from the packet
		isACK = packet.Flags&lib.ACKFlag != 0
		isFIN = packet.Flags&lib.FINFlag != 0
		isDataPacket = len(packet.Payload) > 0
		if isDataPacket {
			// data packet received
			c.handleDataPacket(packet)
		} else { // ACK only or FIN packet
			if isACK {
				// check if 4-way termination is in process
				if c.attrs.TerminationCallerState == lib.CallerFinSent {
					// set the state to callerACKReceived and wait for FIN
					c.attrs.TerminationCallerState = lib.CallerAckReceived
				}
				if c.attrs.TerminationRespState == lib.RespFinSent {
					log.Println("Got ACK from client for my previous FIN packet!")
					// 4-way termination completed
					c.attrs.TerminationRespState = lib.RespAckReceived
					// sent the close signal to pConnClient to clear the connection
					c.pcpService.ConnCloseSignal <- c
					log.Println("4-way termination completed")
					return // this will terminate this go routine gracefully
				}
				// ignore ACK for data packet
			}
			if isFIN {
				if c.attrs.TerminationCallerState == lib.CallerAckReceived {
					// 4-way termination initiated from the client
					c.attrs.TerminationRespState = lib.CallerFinReceived
					// Sent ACK back to the server
					c.acknowledge(packet)
					// set 4-way termination state to CallerFINSent
					c.attrs.TerminationCallerState = lib.CallerAckSent
					// sent the close signal to pConnClient to clear the connection
					c.pcpService.ConnCloseSignal <- c
					return // this will terminate this go routine gracefully
				}
				if c.attrs.TerminationRespState == 0 && c.attrs.TerminationCallerState == 0 {
					log.Println("Got FIN packet from client!")
					// 4-way termination initiated from the server
					c.attrs.TerminationRespState = lib.RespFinReceived
					// Sent ACK back to the server
					c.attrs.LastAckNumber = uint32(uint64(c.attrs.LastAckNumber) + 1)
					ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)
					// Send the acknowledgment packet to the other end
					c.attrs.OutputChan <- ackPacket
					log.Println("ACK to FIN sent.")

					// sent fin packet to the server
					// Assemble FIN packet to be sent to the other end
					finPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.FINFlag, nil, c.attrs)
					c.attrs.OutputChan <- finPacket
					c.attrs.TerminationRespState = lib.RespFinSent
					c.attrs.NextSequenceNumber = uint32(uint64(c.attrs.NextSequenceNumber) + 1) // implicit modulo op
					log.Println("FIN to client sent.")
				}
				//ignore FIN packet in other scenario
			}
		}

	}
}

func (c *Connection) acknowledge(packet *lib.PcpPacket) {
	// prepare a PcpPacket for acknowledging the received packet
	c.attrs.LastAckNumber = uint32(uint64(packet.SequenceNumber) + uint64(len(packet.Payload))) //implicit modulo op
	ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)

	// Send the acknowledgment packet to the other end
	c.attrs.OutputChan <- ackPacket
	log.Println("Acknowledging packet with SEQ", c.attrs.NextSequenceNumber, " and ACQ", c.attrs.LastAckNumber)
}

// Read function for Pcp connection
func (c *Connection) handleDataPacket(packet *lib.PcpPacket) {
	// Extract SYN and ACK flags from the packet
	// for debug purpose
	log.Println("Got packet with SEQ", packet.SequenceNumber, ", and ACK", packet.AcknowledgmentNum)
	// check SEQ and ACK
	if packet.SequenceNumber < c.attrs.LastAckNumber {
		// out of order packet received ignore it
		log.Println("Expect SEQ:", c.attrs.LastAckNumber, ", but got SEQ:", packet.SequenceNumber)
		log.Println("Received out-of-order data packet. Ignore it\n", packet)
		return
	}
	// we ignore ACK info in the received data packet for now

	// put packet payload to read channel
	c.attrs.ReadChannel <- packet.Payload

	// send ACK packet back to the server
	c.acknowledge(packet)
}

// HandleAckPacket handles ACK packet from client during the 3-way handshake.
func (c *Connection) handle3WayHandshake() {
	var (
		packet *lib.PcpPacket
		//err    error
	)
	for {
		packet = <-c.attrs.InputChannel
		// since this go routine only runs for connection initiation
		// so it will only handle ACK message of the SYN-ACK message server sent previously
		// and ignore all other packets
		flags := packet.Flags
		// Extract SYN and ACK flags from the packet
		isSYN := flags&lib.SYNFlag != 0
		isACK := flags&lib.ACKFlag != 0

		// If it's a ACK only packet, handle it
		if !isSYN && isACK {
			// Check if the connection's 3-way handshake state is correct
			if c.OpenServerState+1 != lib.AckReceived {
				log.Printf("3-way handshake state of connection %s is %d, but we received ACK message. Ignore!\n", c.attrs.Key, c.OpenServerState)
				continue
			}
			// 3-way handshaking completed successfully
			// send signal to service for new connection established
			c.pcpService.newConnChannel <- c
			//c.expectedSeqNum don't change
			c.OpenServerState = 0 // reset it
			c.attrs.IsOpenConnection = true

			// handle TCP option negotiation
			if c.attrs.TcpOptions.WindowScaleShiftCount > 0 {
				fmt.Println("Set Window Size with Scaling support!")
				c.attrs.WindowSize = config.WindowSizeWithScale
			}

			return // this will terminate this go routine
		}
	}
}

func (c *Connection) Read(buffer []byte) (int, error) {
	// read packet from connection, blindly acknowledge it and all previous unacknowledged packets since last acknowledged one
	// mimicking net lib TCP read function interface
	payload := <-c.attrs.ReadChannel
	payloadLength := len(payload)
	if payloadLength > len(buffer) {
		err := fmt.Errorf("buffer length (%d) is too short to hold received payload (length %d)", len(buffer), payloadLength)
		log.Println(err)
		return 0, err
	}
	copy(buffer[:payloadLength], payload)
	return payloadLength, nil
}

func (c *Connection) Write(buffer []byte) (int, error) {
	// send out message to the other end
	// mimicking net lib TCP write function interface
	payload := make([]byte, len(buffer))

	if c.attrs.WriteOnHold {
		err := fmt.Errorf("Connection termination in process")
		return 0, err
	}
	// make a copy so that buffer can be overwritten
	copy(payload, buffer)
	// Construct a packet
	packet := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, payload, c.attrs)
	c.attrs.NextSequenceNumber = uint32(uint64(c.attrs.NextSequenceNumber) + uint64(len(payload))) // implicit modulo op
	c.attrs.OutputChan <- packet

	return len(payload), nil
}

func (c *Connection) Close() error {
	// mimicking net lib TCP close function interface
	// initiate connection close by sending FIN to the other side
	// set and check connection's TerminationCallerState along the way of 4-way termination process

	// put write channel on hold so that no data packet interfere with termination process
	c.attrs.WriteOnHold = true
	// Assemble FIN packet to be sent to the other end
	finPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)
	c.attrs.NextSequenceNumber = uint32(uint64(c.attrs.NextSequenceNumber) + 1) // implicit modulo op
	c.attrs.OutputChan <- finPacket
	// set 4-way termination state to CallerFINSent
	c.attrs.TerminationCallerState = lib.CallerFinSent

	return nil
}
