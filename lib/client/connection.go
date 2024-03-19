package client

import (
	"fmt"
	"log"
	"net"

	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type Connection struct {
	key                    string // connection key for easy reference
	RemoteAddr             net.Addr
	RemotePort             int
	LocalAddr              net.Addr
	LocalPort              int
	nextSequenceNumber     uint32                 // the SEQ sequence number of the next outgoing packet
	lastAckNumber          uint32                 // the last acknowleged incoming packet
	InputChannel           chan *lib.PcpPacket    // per connection packet input channel
	OutputChan             chan *lib.PcpPacket    // overall output channel shared by all connections
	readChannel            chan []byte            // for connection read function
	terminationCallerState uint                   // 4-way termination caller states
	terminationRespState   uint                   // 4-way termination responder states
	pcpClientConn          *pcpProtocolConnection // point back to parent
	//expectedAckNum, expectedSeqNum uint32
	writeOnHold bool // true if 4-way termination starts
}

func newConnection(key string, pClientConn *pcpProtocolConnection, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int) (*Connection, error) {
	newConn := &Connection{
		key:                key,
		RemoteAddr:         remoteAddr,
		RemotePort:         remotePort,
		LocalAddr:          localAddr,
		LocalPort:          localPort,
		nextSequenceNumber: 0,
		lastAckNumber:      0,
		InputChannel:       make(chan *lib.PcpPacket),
		readChannel:        make(chan []byte),
		OutputChan:         pClientConn.OutputChan,
		pcpClientConn:      pClientConn,
		// all the rest variables keep there init value
	}

	return newConn, nil
}

func (c *Connection) handleIncomingPackets() {
	var (
		packet                     *lib.PcpPacket
		isACK, isFIN, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		packet = <-c.InputChannel
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
				if c.terminationCallerState == lib.CallerFinSent {
					// set the state to callerACKReceived and wait for FIN
					c.terminationCallerState = lib.CallerAckReceived
				}
				if c.terminationRespState == lib.RespFinSent {
					// 4-way termination completed
					c.terminationRespState = lib.RespAckReceived
					// sent the close signal to pConnClient to clear the connection
					c.pcpClientConn.ConnCloseSignalChan <- c
					return // this will terminate this go routine gracefully
				}
				// ignore ACK for data packet
			}
			if isFIN {
				if c.terminationCallerState == lib.CallerAckReceived {
					log.Println("FIN from server received.")
					// 4-way termination initiated from the client
					c.terminationRespState = lib.CallerFinReceived
					// Sent ACK back to the server

					c.acknowledge(packet)
					log.Println("ACK to FIN from server sent.")
					// set 4-way termination state to CallerFINSent
					c.terminationCallerState = lib.CallerAckSent
					// sent the close signal to pConnClient to clear the connection
					c.pcpClientConn.ConnCloseSignalChan <- c
					return // this will terminate this go routine gracefully
				}
				if c.terminationRespState == 0 && c.terminationCallerState == 0 {
					// put write channel on hold so that no data will interfere with the termination process
					c.writeOnHold = true
					// 4-way termination initiated from the server
					c.terminationRespState = lib.RespFinReceived
					// Sent ACK back to the server
					// Assemble ACK packet to be sent to the other end
					c.lastAckNumber = packet.SequenceNumber + 1
					ackPacket := lib.NewPcpPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.nextSequenceNumber, c.lastAckNumber, lib.ACKFlag, nil)
					// Send the acknowledgment packet to the other end
					c.OutputChan <- ackPacket
					//c.nextSequenceNumber += 1

					// sent fin packet to the server
					// Assemble ACK packet to be sent to the other end
					finPacket := lib.NewPcpPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.nextSequenceNumber, c.lastAckNumber, lib.FINFlag, nil)
					c.OutputChan <- finPacket
					c.terminationRespState = lib.RespFinSent
				}
				//ignore FIN packet in other scenario
			}
		}

	}
}

// mimicking net lib's TCP close client side function
// sending FIN packet to the other end
func (c *Connection) Close() error {
	// put write channel on hold so that no data packet interfere with termination process
	c.writeOnHold = true
	// Assemble FIN packet to be sent to the other end
	finPacket := lib.NewPcpPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.nextSequenceNumber, c.lastAckNumber, lib.FINFlag, nil)
	c.OutputChan <- finPacket
	c.nextSequenceNumber += 1
	// set 4-way termination state to CallerFINSent
	c.terminationCallerState = lib.CallerFinSent

	return nil
}

func (c *Connection) acknowledge(packet *lib.PcpPacket) {
	// prepare a PcpPacket for acknowledging the received packet
	c.lastAckNumber = packet.SequenceNumber + uint32(len(packet.Payload))
	ackPacket := lib.NewPcpPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.nextSequenceNumber, c.lastAckNumber, lib.ACKFlag, nil)

	// Send the acknowledgment packet to the other end
	c.OutputChan <- ackPacket
}

// Read function for Pcp connection
func (c *Connection) handleDataPacket(packet *lib.PcpPacket) {
	// Extract SYN and ACK flags from the packet
	//isACK := packet.Flags&ACKFlag != 0
	// check SEQ and ACK
	if packet.SequenceNumber < c.lastAckNumber {
		// out of order packet received ignore it
		fmt.Println("last acknowledged SEQ:", c.lastAckNumber, ", but got incoming SEQ:", packet.SequenceNumber)
		fmt.Println("Received out-of-order data packet. Ignore it\n", packet)
		return
	}
	// we ignore ACK info in the received data packet for now

	// put packet payload to read channel
	c.readChannel <- packet.Payload

	// send ACK packet back to the server
	c.acknowledge(packet)
}

// connection read function to mimick net lib TCP read function
func (c *Connection) Read(buffer []byte) (int, error) {
	payload := <-c.readChannel
	payloadLength := len(payload)
	if payloadLength > len(buffer) {
		err := fmt.Errorf("buffer length (%d) is too short to hold received payload (length %d)", len(buffer), payloadLength)
		fmt.Println(err)
		return 0, err
	}
	copy(buffer[:payloadLength], payload)
	return payloadLength, nil
}

// connection write function mimicks net lib TCP write function
func (c *Connection) Write(buffer []byte) (int, error) {
	payload := make([]byte, len(buffer))

	if c.writeOnHold {
		err := fmt.Errorf("Connection termination in process")
		return 0, err
	}
	// make a copy so that buffer can be overwritten
	copy(payload, buffer)
	log.Println("Sent payload:", string(payload))
	// Construct a packet
	packet := lib.NewPcpPacket(uint16(c.LocalPort), uint16(c.RemotePort), c.nextSequenceNumber, c.lastAckNumber, 0, payload)
	c.nextSequenceNumber += uint32(len(packet.Payload))

	c.OutputChan <- packet

	return len(payload), nil
}
