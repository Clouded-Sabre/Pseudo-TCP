package client

import (
	"fmt"
	"log"
	"math"
	"net"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
)

type Connection struct {
	attrs         *lib.Connection
	pcpClientConn *pcpProtocolConnection // point back to parent
}

func newConnection(key string, pClientConn *pcpProtocolConnection, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int) (*Connection, error) {
	isn, _ := lib.GenerateISN()
	options := &lib.Options{
		WindowScaleShiftCount: config.WindowScale,
		MSS:                   config.PreferredMss,
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
		OutputChan:         pClientConn.OutputChan,
		// all the rest variables keep there init value
		TcpOptions: options,
	}
	newConn := &Connection{
		attrs:         newAttrs,
		pcpClientConn: pClientConn,
		// all the rest variables keep there init value
	}

	return newConn, nil
}

func (c *Connection) handleIncomingPackets() {
	var (
		packet                            *lib.PcpPacket
		isACK, isFIN, isRST, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		packet = <-c.attrs.InputChannel
		// Extract SYN and ACK flags from the packet
		isACK = packet.Flags&lib.ACKFlag != 0
		isFIN = packet.Flags&lib.FINFlag != 0
		isRST = packet.Flags&lib.RSTFlag != 0
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
					// 4-way termination completed
					c.attrs.TerminationRespState = lib.RespAckReceived
					// sent the close signal to pConnClient to clear the connection
					c.pcpClientConn.ConnCloseSignalChan <- c
					return // this will terminate this go routine gracefully
				}
				// ignore ACK for data packet
			}
			if isFIN {
				if c.attrs.TerminationCallerState == lib.CallerAckReceived {
					log.Println("FIN from server received.")
					// 4-way termination initiated from the client
					c.attrs.TerminationRespState = lib.CallerFinReceived
					// Sent ACK back to the server
					c.attrs.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1)
					ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)
					// Send the acknowledgment packet to the other end
					c.attrs.OutputChan <- ackPacket

					log.Println("ACK to FIN from server sent.")
					// set 4-way termination state to CallerFINSent
					c.attrs.TerminationCallerState = lib.CallerAckSent
					// sent the close signal to pConnClient to clear the connection
					c.pcpClientConn.ConnCloseSignalChan <- c
					return // this will terminate this go routine gracefully
				}
				if c.attrs.TerminationRespState == 0 && c.attrs.TerminationCallerState == 0 {
					// put write channel on hold so that no data will interfere with the termination process
					c.attrs.WriteOnHold = true
					// 4-way termination initiated from the server
					c.attrs.TerminationRespState = lib.RespFinReceived
					// Sent ACK back to the server
					// Assemble ACK packet to be sent to the other end
					c.attrs.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1)
					ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)
					// Send the acknowledgment packet to the other end
					c.attrs.OutputChan <- ackPacket
					//c.nextSequenceNumber += 1

					// sent fin packet to the server
					// Assemble ACK packet to be sent to the other end
					finPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.FINFlag, nil, c.attrs)
					c.attrs.OutputChan <- finPacket
					c.attrs.TerminationRespState = lib.RespFinSent
				}
				//ignore FIN packet in other scenario
			}
			if isRST {
				log.Println(lib.Red + "Got RST packet from server!" + lib.Reset)
			}
		}

	}
}

// mimicking net lib's TCP close client side function
// sending FIN packet to the other end
func (c *Connection) Close() error {
	// put write channel on hold so that no data packet interfere with termination process
	c.attrs.WriteOnHold = true
	// Assemble FIN packet to be sent to the other end
	finPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.FINFlag, nil, c.attrs)
	c.attrs.OutputChan <- finPacket
	c.attrs.NextSequenceNumber = uint32(uint64(c.attrs.NextSequenceNumber) + 1) // implicit modulo op
	// set 4-way termination state to CallerFINSent
	c.attrs.TerminationCallerState = lib.CallerFinSent

	return nil
}

func (c *Connection) acknowledge(packet *lib.PcpPacket) {
	// prepare a PcpPacket for acknowledging the received packet
	c.attrs.LastAckNumber = uint32(uint64(packet.SequenceNumber) + uint64(len(packet.Payload)))
	ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)

	// Send the acknowledgment packet to the other end
	c.attrs.OutputChan <- ackPacket
}

// Read function for Pcp connection
func (c *Connection) handleDataPacket(packet *lib.PcpPacket) {
	// Extract SYN and ACK flags from the packet
	//isACK := packet.Flags&ACKFlag != 0
	// check SEQ and ACK
	if packet.SequenceNumber < c.attrs.LastAckNumber {
		// out of order packet received ignore it
		fmt.Println("last acknowledged SEQ:", c.attrs.LastAckNumber, ", but got incoming SEQ:", packet.SequenceNumber)
		fmt.Println("Received out-of-order data packet. Just resent the last ACK message.\n", packet)
		ackPacket := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, nil, c.attrs)
		// Send the acknowledgment packet to the other end
		c.attrs.OutputChan <- ackPacket
		return
	}
	// we ignore ACK info in the received data packet for now

	// put packet payload to read channel
	c.attrs.ReadChannel <- packet.Payload

	// send ACK packet back to the server
	c.acknowledge(packet)
}

// connection read function to mimick net lib TCP read function
func (c *Connection) Read(buffer []byte) (int, error) {
	payload := <-c.attrs.ReadChannel
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

	if c.attrs.WriteOnHold {
		err := fmt.Errorf("connection termination in process")
		return 0, err
	}
	// make a copy so that buffer can be overwritten
	copy(payload, buffer)
	log.Println("Sent payload:", string(payload))
	// Construct a packet
	packet := lib.NewPcpPacket(c.attrs.NextSequenceNumber, c.attrs.LastAckNumber, lib.ACKFlag, payload, c.attrs)
	c.attrs.NextSequenceNumber = uint32(uint64(c.attrs.NextSequenceNumber) + uint64(len(packet.Payload)))

	c.attrs.OutputChan <- packet

	return len(payload), nil
}
