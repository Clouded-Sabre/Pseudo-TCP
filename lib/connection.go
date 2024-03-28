package lib

import (
	"fmt"
	"log"
	"math"
	"net"
	"time"

	"github.com/Clouded-Sabre/Pseudo-TCP/config"
)

type Connection struct {
	Key                    string // connection key for easy reference
	RemoteAddr             net.Addr
	RemotePort             int
	LocalAddr              net.Addr
	LocalPort              int
	NextSequenceNumber     uint32 // the SEQ sequence number of the next outgoing packet
	LastAckNumber          uint32 // the last acknowleged incoming packet
	WindowSize             uint16
	InputChannel           chan *PcpPacket // per connection packet input channel
	OutputChan             chan *PcpPacket // overall output channel shared by all connections
	ReadChannel            chan []byte     // for connection read function
	TerminationCallerState uint            // 4-way termination caller states
	TerminationRespState   uint            // 4-way termination responder states
	TcpOptions             *Options        // tcp options
	WriteOnHold            bool            // true if 4-way termination starts
	IsOpenConnection       bool            //false if in 3-way handshake
	KeepaliveTimer         *time.Timer     // Timer for keepalive mechanism
	IdleTimeout            time.Duration   // Idle timeout duration
	TimeoutCount           int             // Timeout count for keepalive
	KeepaliveInterval      time.Duration   // Interval between keepalive attempts
	MaxKeepaliveAttempts   int             // Maximum number of keepalive attempts before marking the connection as dead
	IsKeepAliveInProgress  bool            // denote if keepalive probe is in process or not
	IsDead                 bool            // mark the connection is idle timed out and failed keepalive probe

	connCloseSignalChan chan *Connection // send close connection signal to parent service or pConnection to clear it
	newConnChannel      chan *Connection // server only. send new connection signal to parent service to signal successful 3-way handshake
	OpenServerState     uint             // server only. 3-way handshake server states
	closeSigal          chan struct{}    // used to send close signal to HandleIncomingPackets go routine to stop when keepalive failed
}

type Options struct {
	WindowScaleShiftCount uint8  // TCP Window scaling, < 14 which mean WindowSize * 2^14
	MSS                   uint16 // max tcp segment size
	SupportSack           bool   // SACK support. No real support because no retransmission happens
	TimestampEnabled      bool   // timestamp support
	TsEchoReplyValue      uint32
	Timestamp             uint32
}

func NewConnection(key string, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int, outputChan chan *PcpPacket, connCloseSignalChan, newConnChannel chan *Connection) (*Connection, error) {
	isn, _ := GenerateISN()
	options := &Options{
		WindowScaleShiftCount: config.WindowScale,
		MSS:                   config.PreferredMss,
		SupportSack:           true,
		TimestampEnabled:      true,
	}
	newConn := &Connection{
		Key:                key,
		RemoteAddr:         remoteAddr,
		RemotePort:         remotePort,
		LocalAddr:          localAddr,
		LocalPort:          localPort,
		NextSequenceNumber: isn,
		LastAckNumber:      0,
		WindowSize:         math.MaxUint16,
		InputChannel:       make(chan *PcpPacket),
		ReadChannel:        make(chan []byte),
		OutputChan:         outputChan,
		// all the rest variables keep there init value
		TcpOptions:           options,
		IdleTimeout:          time.Second * config.IdleTimeout,
		KeepaliveInterval:    time.Second * config.KeepaliveInterval,
		MaxKeepaliveAttempts: config.MaxKeepaliveAttempts,

		connCloseSignalChan: connCloseSignalChan,
		newConnChannel:      newConnChannel,
		closeSigal:          make(chan struct{}),
	}

	if config.KeepAliveEnabled {
		newConn.KeepaliveTimer = time.NewTimer(config.KeepaliveInterval)
		newConn.startKeepaliveTimer()
	}

	return newConn, nil
}

func (c *Connection) HandleIncomingPackets() {
	var (
		packet                            *PcpPacket
		isACK, isFIN, isRST, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		select {
		case <-c.closeSigal:
			// connection idle timed out. quit this go routine
			return
		case packet = <-c.InputChannel:
			// reset the keepalive timer
			if config.KeepAliveEnabled {
				c.KeepaliveTimer.Reset(c.IdleTimeout)
				c.TimeoutCount = 0
			}

			// Parse TCP options to update timestamp parameters
			if packet.TcpOptions != nil && packet.TcpOptions.TimestampEnabled {
				// Update timestamp parameters
				c.TcpOptions.TsEchoReplyValue = packet.TcpOptions.Timestamp
			}

			// Extract SYN and ACK flags from the packet
			isACK = packet.Flags&ACKFlag != 0
			isFIN = packet.Flags&FINFlag != 0
			isRST = packet.Flags&RSTFlag != 0
			isDataPacket = len(packet.Payload) > 0
			if isDataPacket {
				// data packet received
				c.handleDataPacket(packet)
			} else { // ACK only or FIN packet
				if isACK {
					// check if 4-way termination is in process
					if c.TerminationCallerState == CallerFinSent {
						log.Println("Got ACK from 4-way responder. Wait for FIN from responder")
						// set the state to callerACKReceived and wait for FIN
						c.TerminationCallerState = CallerAckReceived
					}
					if c.TerminationRespState == RespFinSent {
						log.Println("Got ACK from 4-way caller. 4-way completed successfully")
						// 4-way termination completed
						c.TerminationRespState = RespAckReceived
						// sent the close signal to pConnClient to clear the connection
						c.connCloseSignalChan <- c
						return // this will terminate this go routine gracefully
					}
					// ignore ACK for data packet
				}
				if isFIN {
					if c.TerminationCallerState == CallerAckReceived {
						log.Println("Got FIN from 4-way responder.")
						// 4-way termination initiated from the client
						c.TerminationRespState = CallerFinReceived
						// Sent ACK back to the server
						c.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1)
						ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
						// Send the acknowledgment packet to the other end
						c.OutputChan <- ackPacket

						log.Println("ACK to FIN from 4-way responder sent.")
						// set 4-way termination state to CallerFINSent
						c.TerminationCallerState = CallerAckSent
						// sent the close signal to pConnClient to clear the connection
						c.connCloseSignalChan <- c
						return // this will terminate this go routine gracefully
					}
					if c.TerminationRespState == 0 && c.TerminationCallerState == 0 {
						log.Println("Got FIN from 4-way caller.")
						// put write channel on hold so that no data will interfere with the termination process
						c.WriteOnHold = true
						// 4-way termination initiated from the server
						c.TerminationRespState = RespFinReceived
						// Sent ACK back to the server
						// Assemble ACK packet to be sent to the other end
						c.LastAckNumber = uint32(uint64(packet.SequenceNumber) + 1) // implicit modulo included
						ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
						// Send the acknowledgment packet to the other end
						c.OutputChan <- ackPacket
						//c.nextSequenceNumber += 1
						log.Println("Sent ACK to FIN to 4-way caller.")

						// sent FIN packet to the server
						// Assemble FIN packet to be sent to the other end
						finPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, FINFlag, nil, c)
						c.OutputChan <- finPacket
						c.TerminationRespState = RespFinSent
						log.Println("Sent my FIN to 4-way caller.")
					}
					//ignore FIN packet in other scenario
				}
				if isRST {
					log.Println(Red + "Got RST packet from the other end!" + Reset)
				}
			}
		}
	}
}

// Server only. Handle ACK packet from client during the 3-way handshake.
func (c *Connection) Handle3WayHandshake() {
	var (
		packet *PcpPacket
		//err    error
	)
	for {
		packet = <-c.InputChannel
		// since this go routine only runs for connection initiation
		// so it will only handle ACK message of the SYN-ACK message server sent previously
		// and ignore all other packets
		flags := packet.Flags
		// Extract SYN and ACK flags from the packet
		isSYN := flags&SYNFlag != 0
		isACK := flags&ACKFlag != 0

		// If it's a ACK only packet, handle it
		if !isSYN && isACK {
			// Check if the connection's 3-way handshake state is correct
			if c.OpenServerState+1 != AckReceived {
				log.Printf("3-way handshake state of connection %s is %d, but we received ACK message. Ignore!\n", c.Key, c.OpenServerState)
				continue
			}
			// 3-way handshaking completed successfully
			// send signal to service for new connection established
			c.newConnChannel <- c
			//c.expectedSeqNum don't change
			c.OpenServerState = 0 // reset it
			c.IsOpenConnection = true

			// handle TCP option negotiation
			if c.TcpOptions.WindowScaleShiftCount > 0 {
				fmt.Println("Set Window Size with Scaling support!")
				c.WindowSize = config.WindowSizeWithScale
			}

			// handle TCP option timestamp
			if c.TcpOptions.TimestampEnabled {
				c.TcpOptions.TsEchoReplyValue = packet.TcpOptions.Timestamp
			}

			return // this will terminate this go routine
		}
	}
}

// Acknowledge received data packet
func (c *Connection) acknowledge(packet *PcpPacket) {
	// prepare a PcpPacket for acknowledging the received packet
	c.LastAckNumber = uint32(uint64(packet.SequenceNumber) + uint64(len(packet.Payload)))
	ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)

	// Send the acknowledgment packet to the other end
	c.OutputChan <- ackPacket
}

// Handle data packet function for Pcp connection
func (c *Connection) handleDataPacket(packet *PcpPacket) {
	// Extract SYN and ACK flags from the packet
	//isACK := packet.Flags&ACKFlag != 0
	// check SEQ and ACK
	if packet.SequenceNumber < c.LastAckNumber {
		// out of order packet received ignore it
		fmt.Println("last acknowledged SEQ:", c.LastAckNumber, ", but got incoming SEQ:", packet.SequenceNumber)
		fmt.Println("Received out-of-order data packet. Just resent the last ACK message.\n", packet)
		ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
		// Send the acknowledgment packet to the other end
		c.OutputChan <- ackPacket
		return
	}
	// we ignore ACK info in the received data packet for now

	// put packet payload to read channel
	c.ReadChannel <- packet.Payload

	// send ACK packet back to the server
	c.acknowledge(packet)
}

func (c *Connection) Read(buffer []byte) (int, error) {
	// read packet from connection, blindly acknowledge it and all previous unacknowledged packets since last acknowledged one
	// mimicking net lib TCP read function interface
	payload := <-c.ReadChannel
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

	if c.WriteOnHold {
		err := fmt.Errorf("Connection termination in process")
		return 0, err
	}

	totalBytesWritten := 0

	// Iterate over the buffer and split it into segments if necessary
	for len(buffer) > 0 {
		// Determine the length of the current segment
		segmentLength := len(buffer)
		if segmentLength > int(c.TcpOptions.MSS) {
			segmentLength = int(c.TcpOptions.MSS)
		}

		// Extract the current segment from the buffer
		payload := make([]byte, segmentLength)
		copy(payload, buffer[:segmentLength])

		// Construct a packet with the current segment
		packet := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, payload, c)
		c.NextSequenceNumber = uint32(uint64(c.NextSequenceNumber) + uint64(segmentLength)) // Update sequence number. Implicit modulo included
		c.OutputChan <- packet

		// Adjust buffer to exclude the sent segment
		buffer = buffer[segmentLength:]

		// Update total bytes written
		totalBytesWritten += segmentLength
	}

	return totalBytesWritten, nil
}

func (c *Connection) Close() error {
	// mimicking net lib TCP close function interface
	// initiate connection close by sending FIN to the other side
	// set and check connection's TerminationCallerState along the way of 4-way termination process

	// put write channel on hold so that no data packet interfere with termination process
	c.WriteOnHold = true
	// Assemble FIN packet to be sent to the other end
	finPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
	c.NextSequenceNumber = uint32(uint64(c.NextSequenceNumber) + 1) // implicit modulo op
	c.OutputChan <- finPacket
	// set 4-way termination state to CallerFINSent
	c.TerminationCallerState = CallerFinSent

	return nil
}

func (c *Connection) sendKeepalivePacket() {
	// Create and send the keepalive packet
	keepalivePacket := NewPcpPacket(c.NextSequenceNumber-1, c.LastAckNumber, ACKFlag, []byte{0}, c)
	c.OutputChan <- keepalivePacket
	fmt.Println("Sending keepalive packet...")
}

func (c *Connection) startKeepaliveTimer() {
	// Stop the existing timer if it's running
	if c.KeepaliveTimer != nil {
		c.KeepaliveTimer.Stop()
	}

	// Determine the timeout duration
	var timeout time.Duration
	if !c.IsKeepAliveInProgress {
		timeout = c.IdleTimeout
	} else {
		timeout = c.KeepaliveInterval
	}

	// Start the keepalive timer
	c.KeepaliveTimer = time.AfterFunc(timeout, func() {
		if c.TimeoutCount == c.MaxKeepaliveAttempts {
			log.Printf("Connection %s idle timed out. Close it.\n", c.Key)
			// connection idle timed out. Close HandleIncomingPackets go routine first
			c.closeSigal <- struct{}{}
			// then send close connection signal to parent to clear connection resource
			c.connCloseSignalChan <- c

			return
		}
		c.sendKeepalivePacket()
		c.IsKeepAliveInProgress = true
		c.TimeoutCount++
		// Reset the timer to send the next keepalive packet after keepaliveInterval
		c.startKeepaliveTimer()
	})
}
