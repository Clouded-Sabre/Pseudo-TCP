package lib

import (
	"fmt"
	"log"
	"math"
	"net"
	"sort"
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
	ResendPackets          ResendPackets   // data structure to hold sent packets which are not acknowledged yet
	ResendInterval         time.Duration   // packet resend interval
	resendTimer            *time.Timer     // resend timer to trigger resending packet every ResendInterval
	RevPacketCache         PacketGapMap    // Cache for received packets who has gap before it due to packet loss or out-of-order

	connCloseSignalChan chan *Connection // send close connection signal to parent service or pConnection to clear it
	newConnChannel      chan *Connection // server only. send new connection signal to parent service to signal successful 3-way handshake
	OpenServerState     uint             // server only. 3-way handshake server states
	closeSigal          chan struct{}    // used to send close signal to HandleIncomingPackets go routine to stop when keepalive failed
}

type Options struct {
	WindowScaleShiftCount uint8  // TCP Window scaling, < 14 which mean WindowSize * 2^14
	MSS                   uint16 // max tcp segment size
	PermitSack            bool   // SACK permit support. No real support because no retransmission happens
	SackEnabled           bool   // enable SACK option kind 5 or not
	TimestampEnabled      bool   // timestamp support
	TsEchoReplyValue      uint32
	Timestamp             uint32
	InSACKOption          SACKOption // option kind 5 for incoming packets
	OutSACKOption         SACKOption // option kind 5 for outgoing packets
}

type SACKBlock struct {
	LeftEdge  uint32 // Left edge of the SACK block
	RightEdge uint32 // Right edge of the SACK block
}

type SACKOption struct {
	Blocks []SACKBlock // Slice of SACK blocks
}

func NewConnection(key string, remoteAddr net.Addr, remotePort int, localAddr net.Addr, localPort int, outputChan chan *PcpPacket, connCloseSignalChan, newConnChannel chan *Connection) (*Connection, error) {
	isn, _ := GenerateISN()
	options := &Options{
		WindowScaleShiftCount: uint8(config.AppConfig.WindowScale),
		MSS:                   uint16(config.AppConfig.PreferredMSS),
		PermitSack:            config.AppConfig.SackPermitSupport,
		SackEnabled:           config.AppConfig.SackOptionSupport,
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
		IdleTimeout:          time.Second * time.Duration(config.AppConfig.IdleTimeout),
		KeepaliveInterval:    time.Second * time.Duration(config.AppConfig.KeepaliveInterval),
		MaxKeepaliveAttempts: config.AppConfig.MaxKeepaliveAttempts,
		ResendPackets:        *NewResendPackets(),
		ResendInterval:       time.Duration(config.AppConfig.ResendInterval) * time.Millisecond,
		RevPacketCache:       *NewPacketGapMap(),

		connCloseSignalChan: connCloseSignalChan,
		newConnChannel:      newConnChannel,
		closeSigal:          make(chan struct{}),
	}

	if config.AppConfig.KeepAliveEnabled {
		newConn.KeepaliveTimer = time.NewTimer(time.Duration(config.AppConfig.KeepaliveInterval))
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
			if config.AppConfig.KeepAliveEnabled {
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

			// update ResendPackets if it's ACK packet
			if isACK {
				c.UpdateResendPacketsOnAck(packet)
			}

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
				c.WindowSize = uint16(config.AppConfig.WindowSizeWithScale)
			}

			// handle TCP option timestamp
			if c.TcpOptions.TimestampEnabled {
				c.TcpOptions.TsEchoReplyValue = packet.TcpOptions.Timestamp
			}

			//start resend timer if SACK enabled
			if c.TcpOptions.SackEnabled {
				// Start the resend timer
				c.StartResendTimer()
			}

			return // this will terminate this go routine
		}
	}
}

// Acknowledge received data packet
func (c *Connection) acknowledge() {
	// prepare a PcpPacket for acknowledging the received packet
	ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	c.OutputChan <- ackPacket
}

// Handle data packet function for Pcp connection
func (c *Connection) handleDataPacket(packet *PcpPacket) {
	// if the packet was already acknowledged by us, ignore it
	if packet.SequenceNumber < c.LastAckNumber {
		return
	}

	if c.TcpOptions.SackEnabled {
		// if the packet is already in RevPacketCache, ignore it
		if _, found := c.RevPacketCache.GetPacket(packet.SequenceNumber); found {
			return
		}

		c.LastAckNumber, c.TcpOptions.OutSACKOption.Blocks = c.UpdateACKAndSACK(packet)
		// Insert the current packet into RevPacketCache
		c.RevPacketCache.AddPacket(packet)

		// Create a slice to hold packets to delete
		var packetsToDelete []uint32

		// Scan through packets in RevPacketCache
		for _, cachedPacket := range c.RevPacketCache.getPacketsInAscendingOrder() {
			// If a packet's SEQ < c.LastSequenceNumber, put it into ReadChannel
			if cachedPacket.SequenceNumber < c.LastAckNumber {
				c.ReadChannel <- cachedPacket.Payload
				// Add the packet's sequence number to the list of packets to delete
				packetsToDelete = append(packetsToDelete, cachedPacket.SequenceNumber)
			} else {
				// If the packet's SEQ is not less than c.LastSequenceNumber, break the loop
				break
			}
		}

		// Delete packets from RevPacketCache after the loop has finished
		for _, seqNum := range packetsToDelete {
			c.RevPacketCache.RemovePacket(seqNum)
		}
	} else { // SACK support is disabled
		if isGreaterOrEqual(packet.SequenceNumber, c.LastAckNumber) { // throw away out-of-order or lost packets
			// put packet payload to read channel
			c.ReadChannel <- packet.Payload
			c.LastAckNumber = uint32(uint64(packet.SequenceNumber) + uint64(len(packet.Payload)))
		}
	}

	// send ACK packet back to the server
	c.acknowledge()
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

	//fmt.Println("submitting Outgoing packet")

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
		//fmt.Println("buffer length is", len(buffer))

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

// Function to resend lost packets based on SACK blocks and resend packet information
func (c *Connection) resendLostPacket() {
	now := time.Now()
	packetsToRemove := make([]uint32, 0)

	for seqNum, packetInfo := range c.ResendPackets.packets {
		if packetInfo.ResendCount < config.AppConfig.MaxResendCount {
			// Check if the packet has been marked as lost based on SACK blocks
			lost := true
			for _, sackBlock := range c.TcpOptions.InSACKOption.Blocks {
				if isGreaterOrEqual(seqNum, sackBlock.LeftEdge) && isLessOrEqual(seqNum, sackBlock.RightEdge) {
					lost = false
					packetsToRemove = append(packetsToRemove, seqNum)
					break
				}
			}

			// If the packet is marked as lost, check if it's time to resend
			if lost {
				if now.Sub(packetInfo.LastSentTime) >= c.ResendInterval {
					// Resend the packet
					log.Println("One Packet resent!")
					c.OutputChan <- packetInfo.Data
					// Update resend information
					c.ResendPackets.UpdateSentPacket(seqNum)
				}
			}
		} else {
			// Mark the packet for removal if it has been resent too many times
			packetsToRemove = append(packetsToRemove, seqNum)
		}
	}

	// Remove the marked packets outside of the loop
	for _, seqNum := range packetsToRemove {
		c.ResendPackets.RemoveSentPacket(seqNum)
	}
}

// Method to start the resend timer
func (c *Connection) StartResendTimer() {
	c.resendTimer = time.NewTimer(c.ResendInterval)
	go func() {
		defer c.resendTimer.Stop()
		for {
			select {
			case <-c.resendTimer.C:
				c.resendLostPacket()
				c.resendTimer.Reset(c.ResendInterval)
			case <-c.closeSigal:
				c.resendTimer.Stop()
				return
			}
		}
	}()
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

func (c *Connection) UpdateACKAndSACK(packet *PcpPacket) (uint32, []SACKBlock) {
	lastACKNum := c.LastAckNumber
	sackBlocks := c.TcpOptions.OutSACKOption.Blocks
	receivedSEQ := packet.SequenceNumber
	payloadLength := len(packet.Payload)
	if isLess(receivedSEQ, lastACKNum) {
		// Ignore the packet because we have already received and acknowledged the packet
		return lastACKNum, sackBlocks
	}

	if receivedSEQ == lastACKNum {
		// Update last ACK number
		lastACKNum = uint32(uint64(lastACKNum) + uint64(payloadLength))

		// Update SACK blocks if necessary
		if len(sackBlocks) > 0 {
			// since sackBlocks is ordered block, we can simple check if the new lastACKNum
			// touches block 0's leftEdge
			if lastACKNum == sackBlocks[0].LeftEdge {
				// Extend the existing block
				lastACKNum = sackBlocks[0].RightEdge
				// Remove the block as it's fully acknowledged
				sackBlocks = removeSACKBlock(sackBlocks, 0)
			}
		}

		return lastACKNum, sackBlocks
	}

	// receivedSEQ > lastACKNum
	// Update SACK blocks if necessary
	newLeftEdge := receivedSEQ
	newRightEdge := uint32(uint64(receivedSEQ) + uint64(payloadLength))

	var mergedBlocks []SACKBlock
	var insertPosfound bool

	// Iterate through existing SACK blocks to find appropriate actions
	for i, block := range sackBlocks {
		// Case a: If newSEQ and newSEQ+payloadLength fall within an existing block, ignore the packet
		if isGreaterOrEqual(newLeftEdge, block.LeftEdge) && isLessOrEqual(newRightEdge, block.RightEdge) {
			// ignore the packet and return unchanged value
			return lastACKNum, sackBlocks
		}

		// Case b: If newSEQ touches rightEdge of one block and newSEQ+payloadLength touches another block's leftEdge, merge blocks
		if i+1 < len(sackBlocks) {
			if newLeftEdge == block.RightEdge && newRightEdge == sackBlocks[i+1].LeftEdge {
				// Extend the current block to cover both blocks i and i+1
				sackBlocks[i].RightEdge = sackBlocks[i+1].RightEdge
				// Remove block i+1
				sackBlocks = removeSACKBlock(sackBlocks, i+1)

				return lastACKNum, sackBlocks
			}
		}

		// Case c: If only newSEQ touches rightEdge of one block, expand the block's rightEdge
		if newLeftEdge == block.RightEdge {
			sackBlocks[i].RightEdge = newRightEdge
			return lastACKNum, sackBlocks
		}

		// Case d: If only newSEQ+payloadLength touches a block's leftEdge, expand the block's leftEdge
		if newRightEdge == block.LeftEdge {
			sackBlocks[i].LeftEdge = newLeftEdge
			return lastACKNum, sackBlocks
		}

		// case e: need to insert a new block somewhere among the blocks
		if i+1 < len(sackBlocks) {
			if isGreater(newLeftEdge, block.RightEdge) && isLess(newRightEdge, sackBlocks[i+1].LeftEdge) {
				mergedBlocks = append(sackBlocks[:i+1], append([]SACKBlock{{LeftEdge: newLeftEdge, RightEdge: newRightEdge}}, sackBlocks[i+1:]...)...)
				insertPosfound = true
				break
			}
		}
	}

	// If no action was taken, create a new block
	if !insertPosfound {
		// append the new block to the end of the blocks
		sackBlocks = append(sackBlocks, SACKBlock{LeftEdge: newLeftEdge, RightEdge: newRightEdge})
	} else if len(mergedBlocks) > 0 {
		// insert the new block
		sackBlocks = mergedBlocks
	}

	return lastACKNum, sackBlocks
}

// Function to remove a SACK block from the slice
func removeSACKBlock(blocks []SACKBlock, index int) []SACKBlock {
	// If the block to be removed is the last element
	if index == len(blocks)-1 {
		return blocks[:len(blocks)-1]
	}
	// Otherwise, remove the block normally
	copy(blocks[index:], blocks[index+1:])
	return blocks[:len(blocks)-1]
}

func (c *Connection) UpdateResendPacketsOnAck(packet *PcpPacket) {
	// Sort SACK blocks by LeftEdge
	sort.Slice(c.TcpOptions.InSACKOption.Blocks, func(i, j int) bool {
		return isLess(c.TcpOptions.InSACKOption.Blocks[i].LeftEdge, c.TcpOptions.InSACKOption.Blocks[j].LeftEdge)
	})

	// Create a list to store resend packet's SEQ in ascending order
	var seqToRemove []uint32

	// Iterate through ResendPackets
	for seqNum := range c.ResendPackets.packets {
		// Check if the sequence number falls within any SACK block
		for _, sackBlock := range c.TcpOptions.InSACKOption.Blocks {
			if isGreaterOrEqual(seqNum, sackBlock.LeftEdge) && isLessOrEqual(seqNum, sackBlock.RightEdge) {
				// If the sequence number is within a SACK block, mark it for removal
				seqToRemove = append(seqToRemove, seqNum)
				break
			}
		}
		// or, if seqNum < packet.AckNum, also remove it
		if isLess(seqNum, packet.AcknowledgmentNum) {
			seqToRemove = append(seqToRemove, seqNum)
		}
	}

	// Remove marked packets from ResendPackets
	for _, seqNum := range seqToRemove {
		c.ResendPackets.RemoveSentPacket(seqNum)
	}
}

// SEQ compare function with SEQ wraparound in mind
func isGreater(seq1, seq2 uint32) bool {
	// Calculate direct difference
	var diff, wrapdiff, distance int64
	diff = int64(seq1) - int64(seq2)
	if diff < 0 {
		diff = -diff
	}
	wrapdiff = int64(math.MaxUint32 + 1 - diff)

	// Choose the shorter distance
	if diff < wrapdiff {
		distance = diff
	} else {
		distance = wrapdiff
	}

	// Check if the first sequence number is "greater"
	return (distance+int64(seq2))%(math.MaxUint32+1) == int64(seq1)
}

func isGreaterOrEqual(seq1, seq2 uint32) bool {
	return isGreater(seq1, seq2) || (seq1 == seq2)
}

func isLess(seq1, seq2 uint32) bool {
	return !isGreater(seq1, seq2)
}

func isLessOrEqual(seq1, seq2 uint32) bool {
	return isLess(seq1, seq2) || (seq1 == seq2)
}
