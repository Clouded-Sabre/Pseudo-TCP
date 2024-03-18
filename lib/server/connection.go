package server

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type Connection struct {
	key                            string // key of the connection for easy reference
	RemoteAddr                     net.Addr
	RemotePort                     int
	LocalAddr                      net.Addr
	LocalPort                      int
	SequenceNumber                 uint32                 // the last acknowledged sequence number
	InputChannel                   chan *lib.PacketVector // per connection packet input channel
	OutputChan                     chan *lib.PacketVector // overall output channel shared by all connections
	readChannel                    chan []byte            // for connection read function
	OpenServerState                uint                   // 3-way handshake server states
	terminationCallerState         uint                   // 4-way termination caller states
	terminationRespState           uint                   // 4-way termination responder states
	pcpService                     *Service
	expectedAckNum, expectedSeqNum uint32
	writeOnHold                    bool // true if 4-way termination starts
}

var Mu sync.Mutex

func newConnection(key string, srv *Service, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int) (*Connection, error) {
	newConn := &Connection{
		key:            key,
		pcpService:     srv,
		RemoteAddr:     remoteAddr,
		RemotePort:     remotePort,
		LocalAddr:      localAddr,
		LocalPort:      localPort,
		SequenceNumber: 0,
		InputChannel:   make(chan *lib.PacketVector),
		readChannel:    make(chan []byte),
		OutputChan:     srv.OutputChan,
		// all the rest variables keep there init value
	}

	return newConn, nil
}

func (c *Connection) handleIncomingPackets() {
	// Handle client requests - main loop of the connection for handling incoming packets
	// Only cares about data packets and 4-way termination packets
	// if it is a data packet, acknowledge it and any packet which is missing since last acknowledged packet. Then put it into readChannel to be read by Connection.read function
	// if it is a 4-way termination packets call RespHandleCloseConnection
	var (
		packet                     *lib.PacketVector
		isACK, isFIN, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		packet = <-c.InputChannel
		// Extract SYN and ACK flags from the packet
		isACK = packet.Data.Flags&lib.ACKFlag != 0
		isFIN = packet.Data.Flags&lib.FINFlag != 0
		isDataPacket = len(packet.Data.Payload) > 0
		if isDataPacket {
			// data packet received
			c.handleDataPacket(packet)
		} else { // ACK only or FIN packet
			if isACK {
				// check if 4-way termination is in process
				if c.terminationCallerState == lib.CallerFinSent {
					// set the state to callerACKReceived and wait for FIN
					c.terminationCallerState = lib.CallerAckReceived
				}
				if c.terminationRespState == lib.RespFinSent {
					// 4-way termination completed
					c.terminationRespState = lib.RespAckReceived
					// sent the close signal to pConnClient to clear the connection
					c.pcpService.ConnCloseSignal <- c
					return // this will terminate this go routine gracefully
				}
				// ignore ACK for data packet
			}
			if isFIN {
				if c.terminationCallerState == lib.CallerAckReceived {
					// 4-way termination initiated from the client
					c.terminationRespState = lib.CallerFinReceived
					// Sent ACK back to the server
					c.acknowledge(packet)
					// set 4-way termination state to CallerFINSent
					c.terminationCallerState = lib.CallerAckSent
					// sent the close signal to pConnClient to clear the connection
					c.pcpService.ConnCloseSignal <- c
					return // this will terminate this go routine gracefully
				}
				if c.terminationRespState == 0 && c.terminationCallerState == 0 {
					// 4-way termination initiated from the server
					c.terminationRespState = lib.RespFinReceived
					// Sent ACK back to the server
					// Assemble ACK packet to be sent to the other end
					c.acknowledge(packet)

					// sent fin packet to the server
					// Assemble ACK packet to be sent to the other end
					finPacket := lib.NewCustomPacket(uint16(c.LocalPort), uint16(c.RemotePort), 0, 0, lib.FINFlag, nil)
					c.OutputChan <- &lib.PacketVector{Data: finPacket, RemoteAddr: c.RemoteAddr}
					c.terminationRespState = lib.RespFinSent
				}
				//ignore FIN packet in other scenario
			}
		}

	}
}

func (c *Connection) acknowledge(packet *lib.PacketVector) {
	// prepare a CustomPacket for acknowledging the received packet
	ackPacket := lib.NewCustomPacket(uint16(c.LocalPort), uint16(c.RemotePort), 0, packet.Data.AcknowledgmentNum+uint32(len(packet.Data.Payload)), lib.ACKFlag, nil)

	// Send the acknowledgment packet to the other end
	c.OutputChan <- &lib.PacketVector{Data: ackPacket, RemoteAddr: packet.RemoteAddr, LocalAddr: packet.LocalAddr}
	log.Printf("Acknowledging packet with sequence number %d\n", 0)
}

// Read function for Pcp connection
func (c *Connection) handleDataPacket(packet *lib.PacketVector) {
	// Extract SYN and ACK flags from the packet
	//isACK := packet.Flags&ACKFlag != 0
	// check SEQ and ACK
	if packet.Data.SequenceNumber < c.expectedSeqNum {
		// out of order packet received ignore it
		log.Println("Expect SEQ:", c.expectedAckNum, ", but got SEQ:", packet.Data.SequenceNumber)
		log.Println("Received out-of-order data packet. Ignore it\n", packet)
		return
	}
	// we ignore ACK info in the received data packet for now

	// put packet payload to read channel
	c.readChannel <- packet.Data.Payload

	// update expectSeqNum
	c.expectedSeqNum = packet.Data.SequenceNumber + uint32(len(packet.Data.Payload))

	// send ACK packet back to the server
	c.acknowledge(packet)
}

// HandleAckPacket handles ACK packet from client during the 3-way handshake.
func (c *Connection) handle3WayHandshake() {
	var (
		packet *lib.PacketVector
		//err    error
	)
	for {
		packet = <-c.InputChannel
		// since this go routine only runs for connection initiation
		// so it will only handle ACK message of the SYN-ACK message server sent previously
		// and ignore all other packets
		flags := packet.Data.Flags
		// Extract SYN and ACK flags from the packet
		isSYN := flags&lib.SYNFlag != 0
		isACK := flags&lib.ACKFlag != 0

		// If it's a ACK only packet, handle it
		if !isSYN && isACK {
			// Check if the connection's 3-way handshake state is correct
			if c.OpenServerState+1 != lib.AckReceived {
				log.Printf("3-way handshake state of connection %s is %d, but we received ACK message. Ignore!\n", c.key, c.OpenServerState)
				continue
			}
			// 3-way handshaking completed successfully
			// send signal to service for new connection established
			c.pcpService.newConnChannel <- c

			return // this will terminate this go routine
		}
	}
}

func (c *Connection) Read(buffer []byte) (int, error) {
	// read packet from connection, blindly acknowledge it and all previous unacknowledged packets since last acknowledged one
	// mimicking net lib TCP read function interface
	payload := <-c.readChannel
	payloadLength := len(payload)
	if payloadLength > len(buffer) {
		err := fmt.Errorf("buffer length (%d) is too short to hold received payload (length %d)", len(buffer), payloadLength)
		log.Println(err)
		return 0, err
	}
	copy(payload, buffer[:payloadLength])
	return payloadLength, nil
}

func (c *Connection) Wrtie(buffer []byte) (int, error) {
	// send out message to the other end
	// mimicking net lib TCP write function interface
	payload := make([]byte, len(buffer))

	if c.writeOnHold {
		err := fmt.Errorf("Connection termination in process")
		return 0, err
	}
	// make a copy so that buffer can be overwritten
	copy(buffer, payload)
	// Construct a packet
	packet := lib.NewCustomPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.expectedAckNum, c.expectedSeqNum, 0, payload)
	c.OutputChan <- &lib.PacketVector{Data: packet, RemoteAddr: c.RemoteAddr, LocalAddr: c.RemoteAddr}

	return len(payload), nil
}

func (c *Connection) Close() error {
	// mimicking net lib TCP close function interface
	// initiate connection close by sending FIN to the other side
	// set and check connection's TerminationCallerState along the way of 4-way termination process

	// put write channel on hold so that no data packet interfere with termination process
	c.writeOnHold = true
	// Assemble FIN packet to be sent to the other end
	finPacket := lib.NewCustomPacket(uint16(c.LocalPort), uint16(c.RemotePort), 0, 0, lib.ACKFlag, nil)
	c.OutputChan <- &lib.PacketVector{Data: finPacket, RemoteAddr: c.RemoteAddr, LocalAddr: c.LocalAddr}
	// set 4-way termination state to CallerFINSent
	c.terminationCallerState = lib.CallerFinSent

	return nil
}
