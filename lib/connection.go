package lib

import (
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	rp "github.com/Clouded-Sabre/ringpool/lib"
)

type Connection struct {
	// statics
	params         *connectionParams // connection parameters
	config         *ConnectionConfig // connection config
	windowSize     uint16            // PCP windows size, static once connection establishes
	initialSeq     uint32            // connection's initial SEQ, static once connection establishes
	initialPeerSeq uint32            // the initial SEQ from Peer, static once connection establishes

	//variables
	nextSequenceNumber              uint32          // the SEQ sequence number of the next outgoing packet
	lastAckNumber                   uint32          // the last acknowleged incoming packet
	termStartSeq, termStartPeerSeq  uint32          // Seq and Peer Seq when 4-way termination starts
	inputChannel                    chan *PcpPacket // per connection packet input channel
	readChannel                     chan *PcpPacket // for connection read function
	readDeadline                    time.Time       // ReadDeadline for non-blocking read
	terminationCallerState          uint            // 4-way termination caller states
	terminationRespState            uint            // 4-way termination responder states
	initClientState                 uint            // 3-way handshake state of the client
	initServerState                 uint            // 3-way handshake state of the Server
	connSignalTimer                 *time.Timer     // Timer for 3-way handshake and 4-way connection close
	connSignalRetryCount            int             // retry count for 3-way handshake and 4-way connection close
	tcpOptions                      *options        // tcp options
	writeOnHold                     bool            // true if 4-way termination starts
	isOpenConnection                bool            //false if in 3-way handshake
	isBidirectional                 bool            // already seen normal packets from peer
	keepaliveTimer                  *time.Timer     // Timer for keepalive mechanism
	keepaliveTimerMutex             sync.Mutex      // mutex for KeepaliveTimer
	idleTimeout                     time.Duration   // Idle timeout duration
	timeoutCount                    int             // Timeout count for keepalive
	keepaliveInterval               time.Duration   // Interval between keepalive attempts
	isKeepAliveInProgress           bool            // denote if keepalive probe is in process or not
	resendPackets                   ResendPackets   // data structure to hold sent packets which are not acknowledged yet
	resendInterval                  time.Duration   // packet resend interval
	resendTimer                     *time.Timer     // resend timer to trigger resending packet every ResendInterval
	resendTimerMutex                sync.Mutex      // mutex for resendTimer
	revPacketCache                  PacketGapMap    // Cache for received packets who has gap before it due to packet loss or out-of-order
	isClosed                        bool            // denote that the conneciton is closed so that no packets should be accepted
	isClosedMu                      sync.Mutex      // prevent isClosed from concurrent access
	connCloseBegins                 bool            // denote if pcp connection close already began. Used to prevent calling close more than once
	closeSignal                     chan struct{}   // used to send close signal to HandleIncomingPackets go routine to stop when keepalive failed
	connSignalFailed                chan struct{}   // used to notify Connection signalling process (3-way handshake and 4-way termination) failed
	isConnSignalFailedAlreadyClosed bool            // denote if connSignalFailed is already closed to prevent closing it again and cause panic
	wg                              sync.WaitGroup  // wait group for go routine
	// statistics
	rxCount, txCount int64 // count of number of packets received, sent since birth
	rxOooCount       int64 // count of number of received Out-Of-Order packet
}

type options struct {
	windowScaleShiftCount uint8  // TCP Window scaling, < 14 which mean WindowSize * 2^14
	mss                   uint16 // max tcp segment size
	permitSack            bool   // SACK permit support. No real support because no retransmission happens
	SackEnabled           bool   // enable SACK option kind 5 or not
	timestampEnabled      bool   // timestamp support
	tsEchoReplyValue      uint32
	timestamp             uint32
	inSACKOption          sackoption // option kind 5 for incoming packets
	outSACKOption         sackoption // option kind 5 for outgoing packets
}

type sackblock struct {
	leftEdge  uint32 // Left edge of the SACK block
	rightEdge uint32 // Right edge of the SACK block
}

type sackoption struct {
	blocks []sackblock // Slice of SACK blocks
}

type connectionParams struct {
	key                      string           // connection key for easy reference
	isServer                 bool             // server or client role
	remoteAddr, localAddr    net.Addr         // IP addresses
	remotePort, localPort    int              // port number
	outputChan               chan *PcpPacket  // overall output channel shared by all connections
	sigOutputChan            chan *PcpPacket  // overall connection signalling output channel shared by all connections
	connCloseSignalChan      chan *Connection // send close connection signal to parent service or pConnection to clear it
	newConnChannel           chan *Connection // server only. send new connection signal to parent service to signal successful 3-way handshake
	connSignalFailedToParent chan *Connection // used to send signal to parrent to notify connection establishment failed
	pcpCore                  *PcpCore         // PcpCore object
}

// connection config - see config file for detailed explanation
type ConnectionConfig struct {
	WindowScale             int
	PreferredMSS            int
	SackPermitSupport       bool
	SackOptionSupport       bool
	RetransmissionEnabled   bool // whether to enable packet retransmission
	IdleTimeout             int
	KeepAliveEnabled        bool
	KeepaliveInterval       int
	MaxKeepaliveAttempts    int
	ResendInterval          int
	MaxResendCount          int
	Debug                   bool // whether debug mode is on
	WindowSizeWithScale     int
	ConnSignalRetryInterval int
	ConnSignalRetry         int
	ConnectionInputQueue    int
	ShowStatistics          bool
}

func DefaultConnectionConfig() *ConnectionConfig {
	return &ConnectionConfig{
		WindowScale:             14,
		PreferredMSS:            1400,
		SackPermitSupport:       true,
		SackOptionSupport:       true,
		RetransmissionEnabled:   false, // disabled by default
		IdleTimeout:             25,    //seconds
		KeepAliveEnabled:        true,
		KeepaliveInterval:       5, //seconds
		MaxKeepaliveAttempts:    3,
		ResendInterval:          200, //milliseconds
		MaxResendCount:          5,
		WindowSizeWithScale:     12800,
		ConnSignalRetryInterval: 2, //seconds
		ConnSignalRetry:         5,
		ConnectionInputQueue:    1000,
		ShowStatistics:          true,
	}
}

func newConnection(connParams *connectionParams, connConfig *ConnectionConfig) (*Connection, error) {
	isn, _ := GenerateISN()
	options := &options{
		windowScaleShiftCount: uint8(connConfig.WindowScale),
		mss:                   uint16(connConfig.PreferredMSS),
		permitSack:            connConfig.SackPermitSupport,
		SackEnabled:           connConfig.SackOptionSupport,
		timestampEnabled:      true,
	}
	newConn := &Connection{
		config:             connConfig,
		params:             connParams,
		nextSequenceNumber: isn,
		initialSeq:         isn,
		lastAckNumber:      0,
		windowSize:         math.MaxUint16,
		inputChannel:       make(chan *PcpPacket, connConfig.ConnectionInputQueue),
		readChannel:        make(chan *PcpPacket, 200),
		readDeadline:       time.Time{},
		tcpOptions:         options,
		idleTimeout:        time.Second * time.Duration(connConfig.IdleTimeout),
		keepaliveInterval:  time.Second * time.Duration(connConfig.KeepaliveInterval),
		closeSignal:        make(chan struct{}),
		connSignalFailed:   make(chan struct{}),
		wg:                 sync.WaitGroup{},
		isClosedMu:         sync.Mutex{},
	}

	// Only initialize retransmission related fields if enabled
	if connConfig.RetransmissionEnabled {
		newConn.resendPackets = *NewResendPackets()
		newConn.resendInterval = time.Duration(connConfig.ResendInterval) * time.Millisecond
		newConn.revPacketCache = *NewPacketGapMap()
	}

	if connConfig.KeepAliveEnabled {
		newConn.keepaliveTimer = time.NewTimer(time.Duration(connConfig.KeepaliveInterval))
		newConn.startKeepaliveTimer()
	}

	if connConfig.ShowStatistics {
		newConn.wg.Add(1)
		go newConn.StartStatsPrinter()
	}

	return newConn, nil
}

func (c *Connection) LocalAddr() *net.IPAddr {
	return c.params.localAddr.(*net.IPAddr)
}

func (c *Connection) LocalPort() int {
	return c.params.localPort
}

func (c *Connection) RemoteAddr() *net.IPAddr {
	return c.params.remoteAddr.(*net.IPAddr)
}

func (c *Connection) RemotePort() int {
	return c.params.remotePort
}

func (c *Connection) handleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer c.wg.Done()

	var (
		packet                                   *PcpPacket
		isSYN, isACK, isFIN, isRST, isDataPacket bool
		//fp                                       int
		err error
	)
	// Create a loop to read from connection's input channel
	for {
		select {
		case <-c.closeSignal:
			// connection close signal received. quit this go routine
			log.Printf("pcpConnection.handleIncomingPackets: Closing HandleIncomingPackets for connection %s\n", c.params.key)
			return
		case packet = <-c.inputChannel:
			if rp.Debug && packet.GetChunkReference() != nil {
				err = packet.TickChannel()
				if err != nil {
					log.Println("pcpConnection.handleIncomingPackets: ", err)
				}
				//fp = packet.AddFootPrint("Connection.HandleIncomingPackets")
				packet.AddFootPrint("Connection.HandleIncomingPackets")
			}
			// reset the keepalive timer
			if c.config.KeepAliveEnabled {
				c.keepaliveTimerMutex.Lock()
				c.keepaliveTimer.Reset(c.idleTimeout)
				c.keepaliveTimerMutex.Unlock()
				c.timeoutCount = 0
			}

			// Extract SYN and ACK flags from the packet
			isSYN = packet.Flags&SYNFlag != 0
			isACK = packet.Flags&ACKFlag != 0
			isFIN = packet.Flags&FINFlag != 0
			isRST = packet.Flags&RSTFlag != 0
			isDataPacket = len(packet.Payload) > 0

			if !isSYN && !isRST {
				c.isBidirectional = true
				if !c.writeOnHold && c.connSignalTimer != nil {
					c.stopConnSignalTimer()
				}
			}

			// Parse TCP options to update timestamp parameters
			if packet.TcpOptions != nil && packet.TcpOptions.timestampEnabled {
				// Update timestamp parameters
				c.tcpOptions.tsEchoReplyValue = packet.TcpOptions.timestamp
			}

			// update ResendPackets if it's ACK packet
			//if packet.Conn.config.RetransmissionEnabled {
			if c.tcpOptions.SackEnabled && isACK && !isSYN && !isFIN && !isRST {
				c.updateResendPacketsOnAck(packet)
			}
			//}

			if isDataPacket {
				// data packet received
				c.handleDataPacket(packet)
			} else { // ACK only or FIN packet
				if isFIN {
					if c.terminationCallerState == CallerFinSent {
						if isACK && packet.SequenceNumber == c.termStartPeerSeq && packet.AcknowledgmentNum == c.termStartSeq+1 {
							log.Println("pcpConnection.handleIncomingPackets: Got FIN-ACK from 4-way responder.")
							c.stopConnSignalTimer()                               // stop the Connection Signal retry timer
							c.lastAckNumber = SeqIncrement(packet.SequenceNumber) // implicit modulo included
							c.termCallerSendAck()
							log.Println("pcpConnection.handleIncomingPackets: ACK sent to 4-way responder.")
							// clear connection resource and close
							go c.clearConnResource() // has to run it as go routine otherwise it will be block as wg.wait()
							log.Println("pcpConnection.handleIncomingPackets: HandleIncomingPackets stops now.")
							return // this will terminate this go routine gracefully
						}
					}
					if c.terminationRespState == 0 && c.terminationCallerState == 0 {
						log.Println("pcpConnection.handleIncomingPackets: Got FIN from 4-way caller.")
						// put write channel on hold so that no data will interfere with the termination process
						c.writeOnHold = true
						// 4-way termination initiated from the server
						c.terminationRespState = RespFinReceived
						// Sent ACK back to the server
						c.lastAckNumber = SeqIncrement(packet.SequenceNumber) // implicit modulo included
						c.termStartSeq = packet.AcknowledgmentNum
						c.termStartPeerSeq = c.lastAckNumber
						c.termRespSendFinAck()
						c.startConnSignalTimer() // start connection signal retry timer
						log.Println("pcpConnection.handleIncomingPackets: Sent FIN-ACK to 4-way caller.")
					}
					//ignore FIN packet in other scenario
					continue
				}
				if !c.params.isServer && isSYN && !c.isBidirectional {
					//fmt.Println("is SYN!", packet.SequenceNumber, c.InitialPeerSeq)
					if isACK && packet.SequenceNumber == c.initialPeerSeq && packet.AcknowledgmentNum == SeqIncrement(c.initialSeq) {
						// normally on client side SYN-ACK message should be handled in Dial
						// this case is for lost of ACK message from client side which caused a SYN-ACK retry from server
						c.initSendAck() // resend 3-way handshake ACK to peer
					}
					continue
				}
				if isACK {
					// check if 4-way termination is in process
					if c.terminationRespState == RespFinAckSent && packet.SequenceNumber == c.termStartPeerSeq && packet.AcknowledgmentNum == c.termStartSeq+1 {
						log.Println("pcpConnection.handleIncomingPackets: Got ACK from 4-way caller. 4-way completed successfully")
						c.stopConnSignalTimer()
						// 4-way termination completed
						c.terminationRespState = RespAckReceived
						// clear connection resources
						go c.clearConnResource() // has to run it as go routine otherwise it will be block as wg.wait()
						log.Println("pcpConnection.handleIncomingPackets: HandleIncomingPackets stops now.")
						return // this will terminate this go routine gracefully
					}
					// ignore ACK for data packet
					continue
				}
				if isRST {
					log.Println(Red + "pcpConnection.handleIncomingPackets: Got RST packet from the other end!" + Reset)
				}
			}
			//if c.config.Debug && packet.chunk != nil {
			//	packet.chunk.TickFootPrint(fp)
			//}
		}
	}
}

// Server only. Handle ACK packet from client during the 3-way handshake.
func (c *Connection) handle3WayHandshake() {
	// Decrease WaitGroup counter when the goroutine completes
	defer c.wg.Done()

	var (
		packet *PcpPacket
		//err    error
	)
	for {
		select {
		case <-c.connSignalFailed:
			// Connection establishment signalling failed
			// send signal to parent service to remove newConn from tempClientConnections
			c.isClosed = true // no need to use mutex since connection is not ready yet

			if c.connSignalTimer != nil {
				c.stopConnSignalTimer()
				c.connSignalTimer = nil
			}
			log.Println("PcpConnection.handle3WayHandshake: connection establishment failed due to timeout")
			return // this will terminate this go routine
		case packet = <-c.inputChannel:
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
				if c.initServerState+1 != AckReceived {
					log.Printf("PcpConnection.handle3WayHandshake: 3-way handshake state of connection %s is %d, but we received ACK message. Ignore!\n", c.params.key, c.initServerState)
					continue
				}
				c.stopConnSignalTimer() // stops Connection Signal Retry Timer
				// 3-way handshaking completed successfully
				// send signal to service for new connection established
				c.params.newConnChannel <- c
				//c.expectedSeqNum don't change
				c.initServerState = 0 // reset it
				c.isOpenConnection = true

				// handle TCP option negotiation
				if c.tcpOptions.windowScaleShiftCount > 0 {
					log.Println("PcpConnection.handle3WayHandshake: Set Window Size with Scaling support!")
					c.windowSize = uint16(c.config.WindowSizeWithScale)
				}

				// handle TCP option timestamp
				if c.tcpOptions.timestampEnabled {
					c.tcpOptions.tsEchoReplyValue = packet.TcpOptions.timestamp
				}

				// Only start resend timer if retransmission is enabled
				if c.tcpOptions.SackEnabled && c.config.RetransmissionEnabled {
					c.startResendTimer()
				}

				return // this will terminate this go routine
			}
		}
	}
}

// Acknowledge received data packet
func (c *Connection) acknowledge() {
	// prepare a PcpPacket for acknowledging the received packet
	ackPacket := NewPcpPacket(c.nextSequenceNumber, c.lastAckNumber, ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	c.params.outputChan <- ackPacket
}

// Handle data packet function for Pcp connection
func (c *Connection) handleDataPacket(packet *PcpPacket) {
	c.rxCount++
	// if the packet was already acknowledged by us, ignore it
	if packet.SequenceNumber < c.lastAckNumber {
		c.rxOooCount++
		// received packet which we already received and put into readchannel. Ignore it.
		if rp.Debug && packet.GetChunkReference() != nil {
			fp := packet.AddFootPrint("Connection.handleDataPacket")
			packet.TickFootPrint(fp)
		}
		packet.ReturnChunk()
		return
	}

	if c.tcpOptions.SackEnabled && c.config.RetransmissionEnabled {
		// if the packet is already in RevPacketCache, ignore it
		if _, found := c.revPacketCache.GetPacket(packet.SequenceNumber); found {
			if rp.Debug && packet.GetChunkReference() != nil {
				fp := packet.AddFootPrint("Connection.handleDataPacket")
				packet.TickFootPrint(fp)
			}
			packet.ReturnChunk()
			return
		}

		c.lastAckNumber, c.tcpOptions.outSACKOption.blocks = c.updateACKAndSACK(packet)
		// Insert the current packet into RevPacketCache
		c.revPacketCache.AddPacket(packet)

		// Create a slice to hold packets to delete
		var packetsToDelete []uint32

		// Scan through packets in RevPacketCache in descending order of sequence number
		packetsInOrder := c.revPacketCache.getPacketsInAscendingOrder()
		resentTimeOut := c.config.MaxResendCount * c.config.ResendInterval
		for i := len(packetsInOrder) - 1; i >= 0; i-- {
			cachedPacket := packetsInOrder[i]
			// Calculate the age of the packet
			age := time.Since(cachedPacket.ReceivedTime)
			// Check if the age is older than 5 seconds
			if age > time.Duration(resentTimeOut)*time.Millisecond {
				// Find the right edge of the farthest contiguous packet
				rightEdge := SeqIncrementBy(cachedPacket.Packet.SequenceNumber, uint32(len(cachedPacket.Packet.Payload)))
				// Extend the right edge to the farthest contiguous packet's right edge
				for j := i + 1; j < len(packetsInOrder); j++ {
					nextPacket := packetsInOrder[j]
					if rightEdge == nextPacket.Packet.SequenceNumber {
						rightEdge = SeqIncrementBy(nextPacket.Packet.SequenceNumber, uint32(len(nextPacket.Packet.Payload)))
					} else {
						break
					}
				}
				// Update c.LastAckNumber if rightEdge is greater
				if rightEdge > c.lastAckNumber {
					c.lastAckNumber = rightEdge
					c.trimOutSackOption(rightEdge)
				}
				break
			}
		}

		// Scan through packets in RevPacketCache
		for _, cachedPacket := range packetsInOrder {
			// If a packet's SEQ < c.LastSequenceNumber, put it into ReadChannel
			if cachedPacket.Packet.SequenceNumber < c.lastAckNumber {
				// Add the packet's sequence number to the list of packets to delete
				packetsToDelete = append(packetsToDelete, cachedPacket.Packet.SequenceNumber)
			} else {
				// If the packet's SEQ is not less than c.LastSequenceNumber, break the loop
				break
			}
		}

		// Delete packets from RevPacketCache after the loop has finished
		var (
			packetToBeRemoved *ReceivedPacket
			fp                int
		)
		for _, seqNum := range packetsToDelete {
			packetToBeRemoved, _ = c.revPacketCache.GetPacket(seqNum)
			if rp.Debug && packetToBeRemoved.Packet.GetChunkReference() != nil {
				fp = packetToBeRemoved.Packet.AddFootPrint("PCPConnection.handleDataPacket")
				packetToBeRemoved.Packet.TickFootPrint(fp)
				packetToBeRemoved.Packet.AddChannel("pcpconnection.ReadChannel")
			}
			c.readChannel <- packetToBeRemoved.Packet
			c.revPacketCache.RemovePacket(seqNum) // remove the packet from RevPacketCache but do not return the chunk yet
			// chunk will be returned after being read from ReadChannel
		}
	} else { // Simple handling when retransmission or SACK support is disabled
		// put packet payload to read channel regardless if it out-of-order or not, just like UDP
		// higher layer is responsible for handling Out-of-order
		if isGreaterOrEqual(packet.SequenceNumber, c.lastAckNumber) {
			c.lastAckNumber = SeqIncrementBy(packet.SequenceNumber, uint32(len(packet.Payload)))
		}
		if rp.Debug && packet.GetChunkReference() != nil {
			fp := packet.AddFootPrint("Connection.handleDataPacket")
			packet.TickFootPrint(fp)
			packet.AddChannel("c.ReadChannel")
		}
		c.readChannel <- packet
	}

	if rp.Debug && packet.GetChunkReference() != nil {
		log.Printf("Processing data packet: len=%d payload=%x", len(packet.Payload), packet.Payload[:min(16, len(packet.Payload))])
	}

	// send ACK packet back to the server
	c.acknowledge()
}

func (c *Connection) trimOutSackOption(newlastAckNum uint32) {
	// Create a new slice to hold the updated SACK blocks
	var updatedBlocks []sackblock

	// Iterate through each SACK block in OutSACKOption.Blocks
	for _, block := range c.tcpOptions.outSACKOption.blocks {
		// Check if the right edge of the block is greater than lastAckNum
		if block.leftEdge > newlastAckNum {
			// If so, add the block to the updatedBlocks slice
			updatedBlocks = append(updatedBlocks, block)
		}
	}

	// Update OutSACKOption.Blocks with the updatedBlocks slice
	c.tcpOptions.outSACKOption.blocks = updatedBlocks
}

func (c *Connection) Read(buffer []byte) (int, error) {
	// mimicking net lib TCP read function interface
	var (
		packet *PcpPacket
		fp     int
	)

	c.isClosedMu.Lock()
	isClosed := c.isClosed
	c.isClosedMu.Unlock()
	if isClosed || c == nil {
		return 0, io.EOF
	}

	pcpRead := func() (int, error) {

		if packet == nil {
			return 0, io.EOF
		}

		if rp.Debug && packet.GetChunkReference() != nil {
			err := packet.TickChannel()
			if err != nil {
				log.Println("pcpConnection.Read:", err)
			}
			fp = packet.AddFootPrint("Connection.Read")
		}

		payloadLength := len(packet.Payload)
		if payloadLength > len(buffer) {
			err := fmt.Errorf("pcpConnection.Read: buffer length (%d) is too short to hold received payload (length %d)", len(buffer), payloadLength)
			log.Println(err)
			// it's time to return the chunk to pool
			if rp.Debug && packet.GetChunkReference() != nil {
				packet.TickFootPrint(fp)
			}
			packet.ReturnChunk()
			return 0, err
		}
		copy(buffer[:payloadLength], packet.Payload)
		// now that the payload is copied to buffer, we no longer need the packet

		// it's time to return the chunk to pool
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
		}
		packet.ReturnChunk()

		// since packet is returned for all condition branch, there is no need to pop the callstack
		return payloadLength, nil
	}

	if c.readDeadline.IsZero() { // blocking read
		packet = <-c.readChannel
		return pcpRead()
	} else if c.readDeadline.After(time.Now()) {
		select {
		case packet = <-c.readChannel:
			return pcpRead()
		case <-time.After(time.Until(c.readDeadline)):
			return 0, &TimeoutError{"pcpConnection.Read: Read deadline exceeded"}
		}
	} else {
		return 0, fmt.Errorf("pcpConnection.Read: ReadDeadline is in the past")
	}
}

func (c *Connection) SetReadDeadline(t time.Time) error {
	if t.After(time.Now()) || t.IsZero() {
		c.readDeadline = t
		return nil
	} else {
		return fmt.Errorf("pcpConnection.SetReadDeadline: trying to set ReadDeadline to be in the past")
	}
}

func (c *Connection) Write(buffer []byte) (int, error) {
	// send out message to the other end
	// mimicking net lib TCP write function interface
	var fp int

	c.isClosedMu.Lock()
	isClosed := c.isClosed
	c.isClosedMu.Unlock()
	if isClosed || c == nil {
		return 0, io.EOF
	}

	if c.writeOnHold {
		err := fmt.Errorf("pcpConnection.Write: Connection termination in process")
		return 0, err
	}

	totalBytesWritten := 0

	/*if len(buffer) > int(c.tcpOptions.mss) {
		return 0, fmt.Errorf("pcpConnection.Write: buffer length (%d) is too short to hold the payload (length %d) to be written out", int(c.tcpOptions.mss), len(buffer))
	}*/

	c.txCount++

	// Iterate over the buffer and split it into segments if necessary
	for len(buffer) > 0 {
		// Determine the length of the current segment
		segmentLength := len(buffer)
		if segmentLength > int(c.tcpOptions.mss) {
			segmentLength = int(c.tcpOptions.mss)
		}

		// Construct a packet with the current segment
		packet := NewPcpPacket(c.nextSequenceNumber, c.lastAckNumber, ACKFlag, buffer[:segmentLength], c)
		if packet == nil {
			return 0, fmt.Errorf("PcpConnection.Write: failed to create new packet with payload length %d", segmentLength)
		}
		if rp.Debug && packet.GetChunkReference() != nil {
			fp = packet.chunk.AddFootPrint("Connection.Write")
		}

		c.nextSequenceNumber = SeqIncrementBy(c.nextSequenceNumber, uint32(segmentLength)) // Update sequence number. Implicit modulo included
		if rp.Debug && packet.GetChunkReference() != nil {
			packet.TickFootPrint(fp)
			packet.AddChannel("c.OutputChan")
		}
		c.params.outputChan <- packet

		// Adjust buffer to exclude the sent segment
		buffer = buffer[segmentLength:]

		// Update total bytes written
		totalBytesWritten += segmentLength
	}

	return totalBytesWritten, nil
}

// Function to resend lost packets based on SACK blocks and resend packet information
func (c *Connection) resendLostPackets() {
	var fp int

	c.isClosedMu.Lock()
	isClosed := c.isClosed
	c.isClosedMu.Unlock()
	if isClosed {
		return
	}
	now := time.Now()
	packetsToRemove := make([]uint32, 0)
	c.resendPackets.RemovalLock()
	defer c.resendPackets.RemovalUnlock()

	// Get a collection of packet keys for iteration
	keys := c.resendPackets.GetPacketKeys()

	for _, seqNum := range keys {
		packetInfo, ok := c.resendPackets.GetSentPacket(seqNum)
		if !ok {
			continue // Skip this packet if it's not found
		}

		if rp.Debug && packetInfo.Data.GetChunkReference() != nil {
			fp = packetInfo.Data.AddFootPrint("Connection.ResendLostPacket")
		}
		if packetInfo.ResendCount < c.config.MaxResendCount {
			// Check if the packet has been marked as lost based on SACK blocks
			lost := true
			for _, sackBlock := range c.tcpOptions.inSACKOption.blocks {
				if isGreaterOrEqual(seqNum, sackBlock.leftEdge) && isLessOrEqual(seqNum, sackBlock.rightEdge) {
					lost = false
					packetsToRemove = append(packetsToRemove, seqNum)
					break
				}
			}

			// If the packet is marked as lost, check if it's time to resend
			if lost {
				if now.Sub(packetInfo.LastSentTime) >= c.resendInterval {
					// Resend the packet
					log.Printf("PcpConnection.resendLostPackets: One Packet resent with SEQ %d and payload length %d!\n", packetInfo.Data.SequenceNumber-c.initialSeq, len(packetInfo.Data.Payload))
					// make a copy of packetInfo.Data and resend it
					dPacket, err := packetInfo.Data.Duplicate()
					if err != nil {
						log.Println("PcpConnection.resendLostPackets: PCP connection resendLostPacket:", err)
						continue
					}
					if rp.Debug && dPacket.GetChunkReference() != nil {
						fpd := dPacket.AddFootPrint("Connection.ResendLostPacket")
						dPacket.TickFootPrint(fpd)
						dPacket.AddChannel("Connection.outputChan")
					}
					c.params.outputChan <- dPacket
					// Update resend information
					c.resendPackets.UpdateSentPacket(seqNum)
				}
			}
		} else {
			// Mark the packet for removal if it has been resent too many times
			packetsToRemove = append(packetsToRemove, seqNum)
		}

		if c.config.Debug && packetInfo.Data.chunk != nil {
			packetInfo.Data.chunk.TickFootPrint(fp)
		}
	}

	// Remove the marked packets outside of the loop
	for _, seqNum := range packetsToRemove {
		c.resendPackets.RemoveSentPacket(seqNum)
	}
}

// Method to start the resend timer
func (c *Connection) startResendTimer() {
	// Start the keepalive timer
	c.resendTimer = time.AfterFunc(c.resendInterval, func() {
		c.resendTimerMutex.Lock()
		defer c.resendTimerMutex.Unlock()
		if c.resendTimer != nil {
			c.resendTimer.Stop()
			c.resendLostPackets()
			c.startResendTimer()
		}
	})
}

func (c *Connection) sendKeepalivePacket() {
	c.isClosedMu.Lock()
	if c.isClosed {
		c.isClosedMu.Unlock()
		return
	}
	c.isClosedMu.Unlock()
	var fp int
	// Create and send the keepalive packet
	keepalivePacket := NewPcpPacket(c.nextSequenceNumber-1, c.lastAckNumber, ACKFlag, []byte{0}, c)
	if keepalivePacket == nil {
		log.Println("PcpConnection.sendKeepalivePacket: failed to create keepalive message with payload length 1")
		return
	}

	if rp.Debug && keepalivePacket.GetChunkReference() != nil {
		fp = keepalivePacket.chunk.AddFootPrint("connection.sendKeepalivePacket")
		keepalivePacket.chunk.TickFootPrint(fp)
		keepalivePacket.chunk.AddChannel("c.OutputChan")
	}

	keepalivePacket.IsKeepAliveMassege = true
	select {
	case c.params.outputChan <- keepalivePacket:
		fmt.Println("PcpConnection.sendKeepalivePacket: keepalive packet sent...")
	default:
		log.Println("PcpConnection.sendKeepalivePacket: could not send keepalive packet, channel closed or full.")
		// Since we couldn't send, we must return the chunk if we got one.
		keepalivePacket.ReturnChunk()
	}
}

func (c *Connection) startKeepaliveTimer() {
	// Lock to ensure exclusive access to the timer
	c.keepaliveTimerMutex.Lock()
	defer c.keepaliveTimerMutex.Unlock()

	// Stop the existing timer if it's running
	if c.keepaliveTimer != nil {
		c.keepaliveTimer.Stop()
	}

	// Determine the timeout duration
	var timeout time.Duration
	if !c.isKeepAliveInProgress {
		timeout = c.idleTimeout
	} else {
		timeout = c.keepaliveInterval
	}

	// Start the keepalive timer
	c.keepaliveTimer = time.AfterFunc(timeout, func() {
		c.isClosedMu.Lock()
		if c.isClosed {
			c.isClosedMu.Unlock()
			return
		}
		c.isClosedMu.Unlock()
		if c.timeoutCount == c.config.MaxKeepaliveAttempts {
			log.Printf("PcpConnection.startKeepaliveTimer: PCP connection %s idle timed out. Close it.\n", c.params.key)
			// connection idle timed out. clear connection resoureces and close connection
			c.clearConnResource()

			return
		}
		c.sendKeepalivePacket()
		c.isKeepAliveInProgress = true
		c.timeoutCount++
		// Reset the timer to send the next keepalive packet after keepaliveInterval
		c.startKeepaliveTimer()
	})
}
func (c *Connection) updateACKAndSACK(packet *PcpPacket) (uint32, []sackblock) {
	lastACKNum := c.lastAckNumber
	sackBlocks := c.tcpOptions.outSACKOption.blocks
	receivedSEQ := packet.SequenceNumber
	payloadLength := len(packet.Payload)
	if isLess(receivedSEQ, lastACKNum) {
		// Ignore the packet because we have already received and acknowledged the packet
		return lastACKNum, sackBlocks
	}

	if receivedSEQ == lastACKNum {
		// Update last ACK number
		lastACKNum = SeqIncrementBy(lastACKNum, uint32(payloadLength))

		// Update SACK blocks if necessary
		if len(sackBlocks) > 0 {
			// since sackBlocks is ordered block, we can simple check if the new lastACKNum
			// touches block 0's leftEdge
			if lastACKNum == sackBlocks[0].leftEdge {
				// Extend the existing block
				lastACKNum = sackBlocks[0].rightEdge
				// Remove the block as it's fully acknowledged
				sackBlocks = removeSACKBlock(sackBlocks, 0)
			}
		}

		return lastACKNum, sackBlocks
	}

	// receivedSEQ > lastACKNum
	// Update SACK blocks if necessary
	newLeftEdge := receivedSEQ
	newRightEdge := SeqIncrementBy(receivedSEQ, uint32(payloadLength))

	var mergedBlocks []sackblock
	var insertPosfound bool

	// Iterate through existing SACK blocks to find appropriate actions
	for i, block := range sackBlocks {
		// Case a: If newSEQ and newSEQ+payloadLength fall within an existing block, ignore the packet
		if isGreaterOrEqual(newLeftEdge, block.leftEdge) && isLessOrEqual(newRightEdge, block.rightEdge) {
			// ignore the packet and return unchanged value
			return lastACKNum, sackBlocks
		}

		// Case b: If newSEQ touches rightEdge of one block and newSEQ+payloadLength touches another block's leftEdge, merge blocks
		if i+1 < len(sackBlocks) {
			if newLeftEdge == block.rightEdge && newRightEdge == sackBlocks[i+1].leftEdge {
				// Extend the current block to cover both blocks i and i+1
				sackBlocks[i].rightEdge = sackBlocks[i+1].rightEdge
				// Remove block i+1
				sackBlocks = removeSACKBlock(sackBlocks, i+1)

				return lastACKNum, sackBlocks
			}
		}

		// Case c: If only newSEQ touches rightEdge of one block, expand the block's rightEdge
		if newLeftEdge == block.rightEdge {
			sackBlocks[i].rightEdge = newRightEdge
			return lastACKNum, sackBlocks
		}

		// Case d: If only newSEQ+payloadLength touches a block's leftEdge, expand the block's leftEdge
		if newRightEdge == block.leftEdge {
			sackBlocks[i].leftEdge = newLeftEdge
			return lastACKNum, sackBlocks
		}

		// case e: need to insert a new block somewhere among the blocks
		if i+1 < len(sackBlocks) {
			if isGreater(newLeftEdge, block.rightEdge) && isLess(newRightEdge, sackBlocks[i+1].leftEdge) {
				mergedBlocks = append(sackBlocks[:i+1], append([]sackblock{{leftEdge: newLeftEdge, rightEdge: newRightEdge}}, sackBlocks[i+1:]...)...)
				insertPosfound = true
				break
			}
		}
	}

	// If no action was taken, create a new block
	if !insertPosfound {
		// append the new block to the end of the blocks
		sackBlocks = append(sackBlocks, sackblock{leftEdge: newLeftEdge, rightEdge: newRightEdge})
	} else if len(mergedBlocks) > 0 {
		// insert the new block
		sackBlocks = mergedBlocks
	}

	return lastACKNum, sackBlocks
}

// Function to remove a SACK block from the slice
func removeSACKBlock(blocks []sackblock, index int) []sackblock {
	// If the block to be removed is the last element
	if index == len(blocks)-1 {
		return blocks[:len(blocks)-1]
	}
	// Otherwise, remove the block normally
	copy(blocks[index:], blocks[index+1:])
	return blocks[:len(blocks)-1]
}

func (c *Connection) updateResendPacketsOnAck(packet *PcpPacket) {
	var fp int
	if rp.Debug && packet.GetChunkReference() != nil {
		fp = packet.AddFootPrint("connection.UpdateResendPacketsOnAck")
	}
	// Sort SACK blocks by LeftEdge
	sort.Slice(c.tcpOptions.inSACKOption.blocks, func(i, j int) bool {
		return isLess(c.tcpOptions.inSACKOption.blocks[i].leftEdge, c.tcpOptions.inSACKOption.blocks[j].leftEdge)
	})

	// Create a list to store resend packet's SEQ in ascending order
	var seqToRemove []uint32

	c.resendPackets.RemovalLock()
	defer c.resendPackets.RemovalUnlock()

	// Get a collection of packet keys for iteration
	keys := c.resendPackets.GetPacketKeys()

	// Iterate through ResendPackets
	for _, seqNum := range keys {
		_, ok := c.resendPackets.GetSentPacket(seqNum)
		if !ok {
			continue // Skip this packet if it's not found
		}
		// Check if the sequence number falls within any SACK block
		for _, sackBlock := range c.tcpOptions.inSACKOption.blocks {
			if isGreaterOrEqual(seqNum, sackBlock.leftEdge) && isLessOrEqual(seqNum, sackBlock.rightEdge) {
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
		c.resendPackets.RemoveSentPacket(seqNum)
	}

	if rp.Debug && packet.GetChunkReference() != nil {
		packet.TickFootPrint(fp)
	}
}

func (c *Connection) initSendSyn() {
	synPacket := NewPcpPacket(c.initialSeq, 0, SYNFlag, nil, c)
	synPacket.IsOpenConnection = false
	c.params.sigOutputChan <- synPacket
	c.initClientState = SynSent
}

func (c *Connection) initSendSynAck() {
	synAckPacket := NewPcpPacket(c.initialSeq, SeqIncrement(c.initialPeerSeq), SYNFlag|ACKFlag, nil, c)
	synAckPacket.IsOpenConnection = false
	// Send the SYN-ACK packet to the sender
	c.params.sigOutputChan <- synAckPacket
	c.initServerState = SynAckSent // set 3-way handshake state
}

func (c *Connection) initSendAck() {
	ackPacket := NewPcpPacket(SeqIncrement(c.initialSeq), SeqIncrement(c.initialPeerSeq), ACKFlag, nil, c)
	ackPacket.IsOpenConnection = false
	c.params.sigOutputChan <- ackPacket
	c.initClientState = AckSent
}

func (c *Connection) termCallerSendFin() {
	// Assemble FIN packet to be sent to the other end
	finPacket := NewPcpPacket(c.termStartSeq, c.termStartPeerSeq, FINFlag|ACKFlag, nil, c)
	if c.params.sigOutputChan != nil {
		c.params.sigOutputChan <- finPacket
		// set 4-way termination state to CallerFINSent
		c.terminationCallerState = CallerFinSent
	}
}

func (c *Connection) termCallerSendAck() {
	ackPacket := NewPcpPacket(c.termStartSeq+1, c.termStartPeerSeq+1, ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	if c.params.sigOutputChan != nil {
		c.params.sigOutputChan <- ackPacket
		// set 4-way termination state to CallerFINSent
		c.terminationCallerState = CallerAckSent
	}
}

func (c *Connection) termRespSendFinAck() {
	ackPacket := NewPcpPacket(c.termStartSeq, c.termStartPeerSeq, FINFlag|ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	if c.params.sigOutputChan != nil {
		c.params.sigOutputChan <- ackPacket
		c.terminationRespState = RespFinAckSent
	}
}

// start Connection Signal timer to resend signal messages in 3-way handshake and 4-way termination
func (c *Connection) startConnSignalTimer() {
	if c.connSignalTimer != nil {
		c.connSignalTimer.Stop()
	}

	log.Println("PcpConnection.startConnSignalTimer: connSignalTimer started")
	// Restart the timer
	c.connSignalTimer = time.AfterFunc(time.Second*time.Duration(c.config.ConnSignalRetryInterval), func() {
		// Increment ConnSignalRetryCount
		c.connSignalRetryCount++
		log.Printf("PcpConnection.startConnSignalTimer: connSignalTimer fired #%d\n", c.connSignalRetryCount)

		// Check if retries exceed the maximum allowed retries
		if c.connSignalRetryCount >= c.config.ConnSignalRetry {
			// Signal 3-way handshake or 4-way termination failure
			c.connSignalTimer.Stop()
			c.connSignalRetryCount = 0 // reset it to 0
			c.isConnSignalFailedAlreadyClosed = true
			close(c.connSignalFailed) // send failure signal to other function
			return
		}

		// Determine the appropriate packet to resend based on the connection state
		switch {
		case c.initClientState == SynSent:
			// Resend SYN packet
			c.initSendSyn()
		case c.initServerState == SynAckSent:
			// Resend SYN-ACK packet
			c.initSendSynAck()
		case c.terminationCallerState == CallerFinSent:
			// Resend FIN packet from caller side
			c.termCallerSendFin()
		case c.terminationRespState == RespFinAckSent:
			// Resend ACK and FIN packet from responder side
			c.termRespSendFinAck()
		}

		c.startConnSignalTimer()
	})
}

func (c *Connection) stopConnSignalTimer() {
	c.connSignalTimer.Stop()
}

// c.close function wrapped up as go routine
func (c *Connection) CloseAsGoRoutine(wg *sync.WaitGroup) {
	defer wg.Done()

	c.Close()
}

// PCP connection close
func (c *Connection) Close() error {
	if c.isClosed {
		log.Println("PcpConnection.Close: Conection already closed. Return")
		return nil
	}

	if c.connCloseBegins {
		log.Println("PcpConnection.Close: Conection close already began. Return")
		return nil
	}

	c.connCloseBegins = true
	log.Printf("PcpConnection.Close: Initiate termination of pcp connection %s:%d\n", c.params.remoteAddr.(*net.IPAddr).IP, c.params.remotePort)
	err := c.initTermination()
	if err != nil {
		return err
	}
	log.Printf("PcpConnection.Close: Waiting for termination process of pcp connection %s:%d to complete...\n", c.params.remoteAddr.(*net.IPAddr).IP, c.params.remotePort)

	timeout := time.After(6 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			c.isClosedMu.Lock()
			isClosed := c.isClosed
			c.isClosedMu.Unlock()
			if !isClosed {
				log.Printf("PcpConnection.Close: Pcp connection %s:%d timed out waiting for peer closing response. Close the connection forcifully...\n", c.params.remoteAddr.(*net.IPAddr).IP, c.params.remotePort)
				c.clearConnResource()
			}
			return nil
		case <-ticker.C:
			c.isClosedMu.Lock()
			isClosed := c.isClosed
			c.isClosedMu.Unlock()
			if isClosed {
				return nil
			}
		}
	}
}

func (c *Connection) initTermination() error {
	c.writeOnHold = true

	// mimicking net lib TCP close function interfaceisClosed
	c.termStartPeerSeq = c.lastAckNumber
	c.termCallerSendFin()
	c.nextSequenceNumber = SeqIncrement(c.nextSequenceNumber) // implicit modulo op
	c.startConnSignalTimer()

	log.Println("PcpConnection.initTermination: 4-way termination caller sent FIN packet")

	return nil
}

// clear connection resources and send close signal to paranet
func (c *Connection) clearConnResource() {
	c.isClosedMu.Lock()
	isClosed := c.isClosed
	c.isClosedMu.Unlock()
	if isClosed {
		return
	}

	log.Println("PcpConnection.clearConnResource: Start clearing connection resource")
	c.isClosedMu.Lock()
	c.isClosed = true
	c.isClosedMu.Unlock()

	// Lock to ensure exclusive access to the timer
	c.keepaliveTimerMutex.Lock()
	defer c.keepaliveTimerMutex.Unlock()

	// Stop the keepalive timer if it's running
	if c.keepaliveTimer != nil {
		c.keepaliveTimer.Stop()
		c.keepaliveTimer = nil
	}
	log.Println("PcpConnection.clearConnResource: Keepalive timer cleared")

	// Lock to ensure exclusive access to the timer
	c.resendTimerMutex.Lock()
	defer c.resendTimerMutex.Unlock()

	// Stop the resend timer if it's running
	if c.resendTimer != nil {
		c.resendTimer.Stop()
		c.resendTimer = nil
	}
	log.Println("PcpConnection.clearConnResource: Resender timer cleared")

	// stop ConnSignalTimer
	if c.connSignalTimer != nil {
		c.connSignalTimer.Stop()
		c.connSignalTimer = nil
	}
	log.Println("PcpConnection.clearConnResource: ConnSignalTimer timer cleared")

	// Close connection go routines first
	close(c.closeSignal) // Close the channel to signal termination
	log.Println("PcpConnection.clearConnResource: close signal sent to go routine")

	// Drainer Goroutine - drain readChannel so that handleIncomingPackets won't get stuck
	stopChan := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case packet := <-c.readChannel:
				log.Println("PcpConnection.clearConnResource: Draining packet from readChannel")
				if rp.Debug && packet.GetChunkReference() != nil {
					packet.ReturnChunk()
				}
			case <-stopChan:
				return
			}
		}
	}()

	c.wg.Wait() // wait for go routine to close
	log.Println("PcpConnection.clearConnResource: go routine stopped successfully")

	// send signal to drain go routine to stop it
	close(stopChan)
	wg.Wait()
	log.Println("PcpConnection.clearConnResource: drain go routine stopped successfully")

	// close channels
	close(c.inputChannel)
	close(c.readChannel)

	c.params.connCloseSignalChan <- c

	// if SACK option is enabled, release all packets in the sendPackets and RevPacketCache
	if c.tcpOptions.SackEnabled {
		log.Println("PcpConnection.clearConnResource: close signal already sent to parent. Now we start to return all chunks")
		// Return chunks of all packets in c.ResendPackets to pool
		for _, packet := range c.resendPackets.packets {
			packet.Data.ReturnChunk()
		}
		log.Printf("PcpConnection.clearConnResource: released %d chunks from ResendPackets\n", len(c.resendPackets.packets))

		// Return chunks of all packets in c.RevPacketCache to pool
		for _, packet := range c.revPacketCache.packets {
			packet.Packet.ReturnChunk()
		}
		log.Printf("PcpConnection.clearConnResource: Released %d chunks from RevPacketCache\n", len(c.revPacketCache.packets))
	}

	// remove the filtering rule of the client side connection which is used to prevent outgoing RST packet
	if !c.params.isServer { // client side connection
		err := (*c.params.pcpCore.filter).RemoveTcpClientFiltering(c.RemoteAddr().IP.String(), c.RemotePort())
		if err != nil {
			log.Println("PcpConnection.clearConnResource: failed to remove filtering rule from client side connection")
		} else {
			log.Println("PcpConnection.clearConnResource: filtering rule removed from client side connection")
		}
	}

	log.Printf("PcpConnection.clearConnResource %s: resource cleared.\n", c.params.key)
}

func (c *Connection) GetMSS() int {
	return int(c.tcpOptions.mss)
}

func (c *Connection) StartStatsPrinter() {
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			log.Printf("PcpConnecction.StartStatsPrinter: (%s:%d - %s:%d) rxCount: %s%d%s, txCount: %s%d%s, rxOooCount: %s%d%s\n", c.RemoteAddr().IP.String(), c.RemotePort(), c.LocalAddr().IP.String(), c.LocalPort(), ColorGreen, c.rxCount, ColorReset, ColorGreen, c.txCount, ColorReset, ColorMagenta, c.rxOooCount, ColorReset)
		case <-c.closeSignal:
			return
		}
	}
}
