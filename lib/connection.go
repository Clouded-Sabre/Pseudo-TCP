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
)

type Connection struct {
	//Key                            string // connection key for easy reference
	//isServer                       bool
	//RemoteAddr                     net.Addr
	//RemotePort                     int
	//LocalAddr                      net.Addr
	//LocalPort                      int
	NextSequenceNumber             uint32 // the SEQ sequence number of the next outgoing packet
	LastAckNumber                  uint32 // the last acknowleged incoming packet
	WindowSize                     uint16
	InitialSeq                     uint32          // connection's initial SEQ
	InitialPeerSeq                 uint32          // the initial SEQ from Peer
	TermStartSeq, TermStartPeerSeq uint32          // Seq and Peer Seq when 4-way termination starts
	InputChannel                   chan *PcpPacket // per connection packet input channel
	//OutputChan, sigOutputChan      chan *PcpPacket // overall output channel shared by all connections
	ReadChannel            chan *PcpPacket // for connection read function
	ReadDeadline           time.Time       // ReadDeadline for non-blocking read
	TerminationCallerState uint            // 4-way termination caller states
	TerminationRespState   uint            // 4-way termination responder states
	InitClientState        uint            // 3-way handshake state of the client
	InitServerState        uint            // 3-way handshake state of the Server
	ConnSignalTimer        *time.Timer     // Timer for 3-way handshake and 4-way connection close
	ConnSignalRetryCount   int             // retry count for 3-way handshake and 4-way connection close
	TcpOptions             *Options        // tcp options
	WriteOnHold            bool            // true if 4-way termination starts
	IsOpenConnection       bool            //false if in 3-way handshake
	IsBidirectional        bool            // already seen normal packets from peer
	//KeepAliveEnabled               bool            // whether keep alive is enabled
	KeepaliveTimer      *time.Timer   // Timer for keepalive mechanism
	KeepaliveTimerMutex sync.Mutex    // mutex for KeepaliveTimer
	IdleTimeout         time.Duration // Idle timeout duration
	TimeoutCount        int           // Timeout count for keepalive
	KeepaliveInterval   time.Duration // Interval between keepalive attempts
	//MaxKeepaliveAttempts           int             // Maximum number of keepalive attempts before marking the connection as dead
	IsKeepAliveInProgress bool          // denote if keepalive probe is in process or not
	IsDead                bool          // mark the connection is idle timed out and failed keepalive probe
	ResendPackets         ResendPackets // data structure to hold sent packets which are not acknowledged yet
	ResendInterval        time.Duration // packet resend interval
	resendTimer           *time.Timer   // resend timer to trigger resending packet every ResendInterval
	resendTimerMutex      sync.Mutex    // mutex for resendTimer
	RevPacketCache        PacketGapMap  // Cache for received packets who has gap before it due to packet loss or out-of-order
	IsClosed              bool          // denote that the conneciton is closed so that no packets should be accepted

	//connCloseSignalChan      chan *Connection // send close connection signal to parent service or pConnection to clear it
	//connSignalFailedToParent chan *Connection // used to send signal to parrent to notify connection establishment failed
	//newConnChannel           chan *Connection // server only. send new connection signal to parent service to signal successful 3-way handshake
	closeSigal       chan struct{}  // used to send close signal to HandleIncomingPackets go routine to stop when keepalive failed
	ConnSignalFailed chan struct{}  // used to notify Connection signalling process (3-way handshake and 4-way termination) failed
	Wg               sync.WaitGroup // wait group for go routine
	//Debug                    bool             // whether debug mode is on
	//WindowSizeWithScale      int              // Window Size with scale
	config *ConnectionConfig // connection config
	Params *ConnectionParams
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

type ConnectionParams struct {
	Key                      string
	IsServer                 bool
	RemoteAddr, LocalAddr    net.Addr
	RemotePort, LocalPort    int
	OutputChan               chan *PcpPacket
	SigOutputChan            chan *PcpPacket
	ConnCloseSignalChan      chan *Connection
	NewConnChannel           chan *Connection
	ConnSignalFailedToParent chan *Connection
}

type ConnectionConfig struct {
	WindowScale             int
	PreferredMSS            int
	SackPermitSupport       bool
	SackOptionSupport       bool
	IdleTimeout             int
	KeepAliveEnabled        bool
	KeepaliveInterval       int
	MaxKeepaliveAttempts    int
	ResendInterval          int
	MaxResendCount          int
	Debug                   bool
	WindowSizeWithScale     int
	ConnSignalRetryInterval int
	ConnSignalRetry         int
}

func NewConnection(connParams *ConnectionParams, connConfig *ConnectionConfig) (*Connection, error) {
	isn, _ := GenerateISN()
	options := &Options{
		WindowScaleShiftCount: uint8(connConfig.WindowScale),
		MSS:                   uint16(connConfig.PreferredMSS),
		PermitSack:            connConfig.SackPermitSupport,
		SackEnabled:           connConfig.SackOptionSupport,
		TimestampEnabled:      true,
	}
	newConn := &Connection{
		config: connConfig,
		Params: connParams,
		//Key:                connConfig.Key,
		//isServer:           connConfig.IsServer,
		//RemoteAddr:         connConfig.RemoteAddr,
		//RemotePort:         connConfig.RemotePort,
		//LocalAddr:          connConfig.LocalAddr,
		//LocalPort:          connConfig.LocalPort,
		NextSequenceNumber: isn,
		InitialSeq:         isn,
		LastAckNumber:      0,
		WindowSize:         math.MaxUint16,
		InputChannel:       make(chan *PcpPacket),
		ReadChannel:        make(chan *PcpPacket),
		ReadDeadline:       time.Time{},
		//OutputChan:         connConfig.OutputChan,
		//sigOutputChan:      connConfig.SigOutputChan,
		// all the rest variables keep there init value
		TcpOptions:  options,
		IdleTimeout: time.Second * time.Duration(connConfig.IdleTimeout),
		//KeepAliveEnabled:     connConfig.KeepAliveEnabled,
		KeepaliveInterval: time.Second * time.Duration(connConfig.KeepaliveInterval),
		//MaxKeepaliveAttempts: connConfig.MaxKeepaliveAttempts,
		ResendPackets:  *NewResendPackets(),
		ResendInterval: time.Duration(connConfig.ResendInterval) * time.Millisecond,
		RevPacketCache: *NewPacketGapMap(),

		//connCloseSignalChan:      connConfig.ConnCloseSignalChan,
		//connSignalFailedToParent: connConfig.ConnSignalFailedToParent,
		//newConnChannel:           connConfig.NewConnChannel,
		closeSigal:       make(chan struct{}),
		ConnSignalFailed: make(chan struct{}),
		Wg:               sync.WaitGroup{},
		//Debug:                    connConfig.Debug,
		//WindowSizeWithScale:      connConfig.WindowSizeWithScale,
	}

	if connConfig.KeepAliveEnabled {
		newConn.KeepaliveTimer = time.NewTimer(time.Duration(connConfig.KeepaliveInterval))
		newConn.startKeepaliveTimer()
	}

	return newConn, nil
}

func (c *Connection) HandleIncomingPackets() {
	// Decrease WaitGroup counter when the goroutine completes
	defer c.Wg.Done()

	var (
		packet                                   *PcpPacket
		isSYN, isACK, isFIN, isRST, isDataPacket bool
	)
	// Create a loop to read from connection's input channel
	for {
		select {
		case <-c.closeSigal:
			// connection close signal received. quit this go routine
			log.Printf("Closing HandleIncomingPackets for connection %s\n", c.Params.Key)
			return
		case packet = <-c.InputChannel:
			if c.config.Debug && packet.chunk != nil {
				packet.chunk.RemoveFromChannel()
				packet.chunk.AddCallStack("Connection.HandleIncomingPackets")
			}
			// reset the keepalive timer
			if c.config.KeepAliveEnabled {
				c.KeepaliveTimerMutex.Lock()
				c.KeepaliveTimer.Reset(c.IdleTimeout)
				c.KeepaliveTimerMutex.Unlock()
				c.TimeoutCount = 0
			}

			// Extract SYN and ACK flags from the packet
			isSYN = packet.Flags&SYNFlag != 0
			isACK = packet.Flags&ACKFlag != 0
			isFIN = packet.Flags&FINFlag != 0
			isRST = packet.Flags&RSTFlag != 0
			isDataPacket = len(packet.Payload) > 0

			/*// if the role is client, filter out 3-way handshake's ACK message from server, which is invalid
			if !c.isServer && packet.Flags == ACKFlag && len(packet.Payload) == 0 {
				if (packet.SequenceNumber == seqIncrement(c.InitialPeerSeq) &&
					(packet.AcknowledgmentNum == seqIncrement(c.InitialSeq) {
						// skip this packet because it's invalid
						continue
					}
			}*/

			if !isSYN && !isRST {
				c.IsBidirectional = true
				if !c.WriteOnHold && c.ConnSignalTimer != nil {
					c.StopConnSignalTimer()
				}
			}

			// Parse TCP options to update timestamp parameters
			if packet.TcpOptions != nil && packet.TcpOptions.TimestampEnabled {
				// Update timestamp parameters
				c.TcpOptions.TsEchoReplyValue = packet.TcpOptions.Timestamp
			}

			// update ResendPackets if it's ACK packet
			if isACK && !isSYN && !isFIN && !isRST {
				c.UpdateResendPacketsOnAck(packet)
			}

			if isDataPacket {
				// data packet received
				c.handleDataPacket(packet)
			} else { // ACK only or FIN packet
				if isFIN {
					if c.TerminationCallerState == CallerFinSent {
						if isACK && packet.SequenceNumber == c.TermStartPeerSeq && packet.AcknowledgmentNum == c.TermStartSeq+1 {
							log.Println("Got FIN-ACK from 4-way responder.")
							c.StopConnSignalTimer()                               // stop the Connection Signal retry timer
							c.LastAckNumber = SeqIncrement(packet.SequenceNumber) // implicit modulo included
							c.TermCallerSendAck()
							log.Println("ACK sent to 4-way responder.")
							// clear connection resource and close
							go c.ClearConnResource() // has to run it as go routine otherwise it will be block as wg.wait()
							log.Println("HandleIncomingPackets stops now.")
							return // this will terminate this go routine gracefully
						}
					}
					if c.TerminationRespState == 0 && c.TerminationCallerState == 0 {
						log.Println("Got FIN from 4-way caller.")
						// put write channel on hold so that no data will interfere with the termination process
						c.WriteOnHold = true
						// 4-way termination initiated from the server
						c.TerminationRespState = RespFinReceived
						// Sent ACK back to the server
						c.LastAckNumber = SeqIncrement(packet.SequenceNumber) // implicit modulo included
						c.TermStartSeq = packet.AcknowledgmentNum
						c.TermStartPeerSeq = c.LastAckNumber
						c.TermRespSendFinAck()
						c.StartConnSignalTimer() // start connection signal retry timer
						log.Println("Sent FIN-ACK to 4-way caller.")
					}
					//ignore FIN packet in other scenario
					continue
				}
				if !c.Params.IsServer && isSYN && !c.IsBidirectional {
					//fmt.Println("is SYN!", packet.SequenceNumber, c.InitialPeerSeq)
					if isACK && packet.SequenceNumber == c.InitialPeerSeq && packet.AcknowledgmentNum == SeqIncrement(c.InitialSeq) {
						// normally on client side SYN-ACK message should be handled in Dial
						// this case is for lost of ACK message from client side which caused a SYN-ACK retry from server
						c.InitSendAck() // resend 3-way handshake ACK to peer
					}
					continue
				}
				if isACK {
					// check if 4-way termination is in process
					if c.TerminationRespState == RespFinAckSent && packet.SequenceNumber == c.TermStartPeerSeq && packet.AcknowledgmentNum == c.TermStartSeq+1 {
						log.Println("Got ACK from 4-way caller. 4-way completed successfully")
						c.StopConnSignalTimer()
						// 4-way termination completed
						c.TerminationRespState = RespAckReceived
						// clear connection resources
						go c.ClearConnResource() // has to run it as go routine otherwise it will be block as wg.wait()
						log.Println("HandleIncomingPackets stops now.")
						return // this will terminate this go routine gracefully
					}
					// ignore ACK for data packet
					continue
				}
				if isRST {
					log.Println(Red + "Got RST packet from the other end!" + Reset)
				}
			}
			if c.config.Debug && packet.chunk != nil {
				packet.chunk.PopCallStack()
			}
		}
	}
}

// Server only. Handle ACK packet from client during the 3-way handshake.
func (c *Connection) Handle3WayHandshake() {
	// Decrease WaitGroup counter when the goroutine completes
	defer c.Wg.Done()

	var (
		packet *PcpPacket
		//err    error
	)
	for {
		select {
		case <-c.ConnSignalFailed:
			// Connection establishment signalling failed
			// send signal to parent service to remove newConn from tempClientConnections
			c.IsClosed = true

			if c.ConnSignalTimer != nil {
				c.StopConnSignalTimer()
				c.ConnSignalTimer = nil
			}
			log.Println("PCP connection establishment failed due to timeout")
			return // this will terminate this go routine
		case packet = <-c.InputChannel:
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
				if c.InitServerState+1 != AckReceived {
					log.Printf("3-way handshake state of connection %s is %d, but we received ACK message. Ignore!\n", c.Params.Key, c.InitServerState)
					continue
				}
				c.StopConnSignalTimer() // stops Connection Signal Retry Timer
				// 3-way handshaking completed successfully
				// send signal to service for new connection established
				c.Params.NewConnChannel <- c
				//c.expectedSeqNum don't change
				c.InitServerState = 0 // reset it
				c.IsOpenConnection = true

				// handle TCP option negotiation
				if c.TcpOptions.WindowScaleShiftCount > 0 {
					fmt.Println("Set Window Size with Scaling support!")
					c.WindowSize = uint16(c.config.WindowSizeWithScale)
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
}

// Acknowledge received data packet
func (c *Connection) acknowledge() {
	// prepare a PcpPacket for acknowledging the received packet
	ackPacket := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	c.Params.OutputChan <- ackPacket
}

// Handle data packet function for Pcp connection
func (c *Connection) handleDataPacket(packet *PcpPacket) {
	if c.config.Debug && packet.chunk != nil {
		packet.chunk.AddCallStack("Connection.handleDataPacket")
	}
	// if the packet was already acknowledged by us, ignore it
	if packet.SequenceNumber < c.LastAckNumber {
		// received packet which we already received and put into readchannel. Ignore it.
		packet.ReturnChunk()
		return
	}

	if c.TcpOptions.SackEnabled {
		// if the packet is already in RevPacketCache, ignore it
		if _, found := c.RevPacketCache.GetPacket(packet.SequenceNumber); found {
			packet.ReturnChunk()
			return
		}

		c.LastAckNumber, c.TcpOptions.OutSACKOption.Blocks = c.UpdateACKAndSACK(packet)
		// Insert the current packet into RevPacketCache
		c.RevPacketCache.AddPacket(packet)

		// Create a slice to hold packets to delete
		var packetsToDelete []uint32

		// Scan through packets in RevPacketCache in descending order of sequence number
		packetsInOrder := c.RevPacketCache.getPacketsInAscendingOrder()
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
				if rightEdge > c.LastAckNumber {
					c.LastAckNumber = rightEdge
					c.trimOutSackOption(rightEdge)
				}
				break
			}
		}

		// Scan through packets in RevPacketCache
		for _, cachedPacket := range packetsInOrder {
			// If a packet's SEQ < c.LastSequenceNumber, put it into ReadChannel
			if cachedPacket.Packet.SequenceNumber < c.LastAckNumber {
				// Add the packet's sequence number to the list of packets to delete
				packetsToDelete = append(packetsToDelete, cachedPacket.Packet.SequenceNumber)
			} else {
				// If the packet's SEQ is not less than c.LastSequenceNumber, break the loop
				break
			}
		}

		// Delete packets from RevPacketCache after the loop has finished
		var packetToBeRemoved *ReceivedPacket
		for _, seqNum := range packetsToDelete {
			packetToBeRemoved, _ = c.RevPacketCache.GetPacket(seqNum)
			if c.config.Debug && packet.chunk != nil {
				packetToBeRemoved.Packet.chunk.AddToChannel("c.ReadChannel")
			}
			c.ReadChannel <- packetToBeRemoved.Packet
			c.RevPacketCache.RemovePacket(seqNum) // remove the packet from RevPacketCache but do not return the chunk yet
			// chunk will be returned after being read from ReadChannel
		}
	} else { // SACK support is disabled
		if isGreaterOrEqual(packet.SequenceNumber, c.LastAckNumber) { // throw away out-of-order or lost packets
			// put packet payload to read channel
			c.ReadChannel <- packet
			if c.config.Debug && packet.chunk != nil {
				packet.chunk.AddToChannel("c.ReadChannel")
			}
			c.LastAckNumber = SeqIncrementBy(packet.SequenceNumber, uint32(len(packet.Payload)))
		} else {
			// We ignore out-of-order packets, so it's time to
			// return its chunk to pool
			packet.ReturnChunk()
		}
	}

	// send ACK packet back to the server
	c.acknowledge()

	if c.config.Debug && packet.chunk != nil {
		packet.chunk.PopCallStack()
	}
}

func (c *Connection) trimOutSackOption(newlastAckNum uint32) {
	// Create a new slice to hold the updated SACK blocks
	var updatedBlocks []SACKBlock

	// Iterate through each SACK block in OutSACKOption.Blocks
	for _, block := range c.TcpOptions.OutSACKOption.Blocks {
		// Check if the right edge of the block is greater than lastAckNum
		if block.LeftEdge > newlastAckNum {
			// If so, add the block to the updatedBlocks slice
			updatedBlocks = append(updatedBlocks, block)
		}
	}

	// Update OutSACKOption.Blocks with the updatedBlocks slice
	c.TcpOptions.OutSACKOption.Blocks = updatedBlocks
}

func (c *Connection) Read(buffer []byte) (int, error) {
	// mimicking net lib TCP read function interface
	var packet *PcpPacket
	if c == nil {
		return 0, io.EOF
	}

	pcpRead := func() (int, error) {

		if packet == nil {
			return 0, io.EOF
		}

		if c.config.Debug && packet.chunk != nil {
			packet.chunk.RemoveFromChannel()
			packet.chunk.AddCallStack("Connection.Read")
		}

		payloadLength := len(packet.Payload)
		if payloadLength > len(buffer) {
			err := fmt.Errorf("buffer length (%d) is too short to hold received payload (length %d)", len(buffer), payloadLength)
			log.Println(err)
			// it's time to return the chunk to pool
			packet.ReturnChunk()
			return 0, err
		}
		copy(buffer[:payloadLength], packet.Payload)
		// now that the payload is copied to buffer, we no longer need the packet
		// it's time to return the chunk to pool
		packet.ReturnChunk()

		// since packet is returned for all condition branch, there is no need to pop the callstack
		return payloadLength, nil
	}

	if c.ReadDeadline.IsZero() { // blocking read
		packet = <-c.ReadChannel
		return pcpRead()
	} else if c.ReadDeadline.After(time.Now()) {
		select {
		case packet = <-c.ReadChannel:
			return pcpRead()
		case <-time.After(time.Until(c.ReadDeadline)):
			return 0, &TimeoutError{"Read deadline exceeded"}
		}
	} else {
		return 0, fmt.Errorf("ReadDeadline is in the past")
	}
}

func (c *Connection) SetReadDeadline(t time.Time) error {
	if t.After(time.Now()) || t.IsZero() {
		c.ReadDeadline = t
		return nil
	} else {
		return fmt.Errorf("trying to set ReadDeadline to be in the past")
	}
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

		// Construct a packet with the current segment
		packet := NewPcpPacket(c.NextSequenceNumber, c.LastAckNumber, ACKFlag, buffer[:segmentLength], c)
		if c.config.Debug && packet.chunk != nil {
			packet.chunk.AddCallStack("Connection.Write")
		}

		c.NextSequenceNumber = SeqIncrementBy(c.NextSequenceNumber, uint32(segmentLength)) // Update sequence number. Implicit modulo included
		c.Params.OutputChan <- packet
		//fmt.Println("buffer length is", len(buffer))
		if c.config.Debug && packet.chunk != nil {
			packet.chunk.AddToChannel("c.OutputChan")
		}

		// Adjust buffer to exclude the sent segment
		buffer = buffer[segmentLength:]

		// Update total bytes written
		totalBytesWritten += segmentLength

		if c.config.Debug && packet.chunk != nil {
			packet.chunk.PopCallStack()
		}
	}

	return totalBytesWritten, nil
}

func (c *Connection) CloseForcefully(wg *sync.WaitGroup) error {
	defer wg.Done()
	err := c.Close()
	if err != nil {
		return err
	}

	SleepForMs(6000)
	if !c.IsClosed {
		c.ClearConnResource()
	}
	return nil
}

func (c *Connection) Close() error {
	// mimicking net lib TCP close function interface
	// initiate connection close by sending FIN to the other side
	// set and check connection's TerminationCallerState along the way of 4-way termination process

	// put write channel on hold so that no data packet interfere with termination process
	c.WriteOnHold = true

	// send syn packet to peer
	c.TermStartSeq = c.NextSequenceNumber
	c.TermStartPeerSeq = c.LastAckNumber
	c.TermCallerSendFin()
	c.NextSequenceNumber = SeqIncrement(c.NextSequenceNumber) // implicit modulo op
	c.StartConnSignalTimer()

	log.Println("4-way termination caller sent FIN packet")

	return nil
}

// clear connection resources and send close signal to paranet
func (c *Connection) ClearConnResource() {
	log.Println("Start clearing connection resource")
	c.IsClosed = true

	// Lock to ensure exclusive access to the timer
	c.KeepaliveTimerMutex.Lock()
	defer c.KeepaliveTimerMutex.Unlock()

	// Stop the keepalive timer if it's running
	if c.KeepaliveTimer != nil {
		c.KeepaliveTimer.Stop()
		c.KeepaliveTimer = nil
	}
	log.Println("Keepalive timer cleared")

	// Lock to ensure exclusive access to the timer
	c.resendTimerMutex.Lock()
	defer c.resendTimerMutex.Unlock()

	// Stop the resend timer if it's running
	if c.resendTimer != nil {
		c.resendTimer.Stop()
		c.resendTimer = nil
	}
	log.Println("Resender timer cleared")

	// stop ConnSignalTimer
	if c.ConnSignalTimer != nil {
		c.ConnSignalTimer.Stop()
		c.ConnSignalTimer = nil
	}
	log.Println("ConnSignalTimer timer cleared")

	// Close connection go routines first
	close(c.closeSigal) // Close the channel to signal termination
	log.Println("close signal sent to go routine")

	c.Wg.Wait() // wait for go routine to close

	// close channels
	close(c.InputChannel)
	close(c.ReadChannel)

	endOfConnClose := func() {
		log.Println("Connection close signal already sent to parent. Now we start to return all chunks")
		// if SACK option is enabled, release all packets in the sendPackets and RevPacketCache
		if c.TcpOptions.SackEnabled {
			// Return chunks of all packets in c.ResendPackets to pool
			for _, packet := range c.ResendPackets.packets {
				packet.Data.ReturnChunk()
			}
			log.Printf("Released %d chunks from ResendPackets\n", len(c.ResendPackets.packets))

			// Return chunks of all packets in c.RevPacketCache to pool
			for _, packet := range c.RevPacketCache.packets {
				packet.Packet.ReturnChunk()
			}
			log.Printf("Released %d chunks from RevPacketCache\n", len(c.RevPacketCache.packets))
		}
		log.Printf("Connection %s resource cleared.\n", c.Params.Key)
	}

	// then send close connection signal to parent to clear connection resource
	defer func() {
		if r := recover(); r != nil {
			// Handle the panic caused by sending to a closed channel
			fmt.Println("Parent is already closed. No need to notify it anymore", r)
			endOfConnClose()
		}
	}()
	c.Params.ConnCloseSignalChan <- c

	endOfConnClose()
}

// Function to resend lost packets based on SACK blocks and resend packet information
func (c *Connection) resendLostPacket() {
	if c.IsClosed {
		return
	}
	now := time.Now()
	packetsToRemove := make([]uint32, 0)
	c.ResendPackets.RemovalLock()
	defer c.ResendPackets.RemovalUnlock()

	// Get a collection of packet keys for iteration
	keys := c.ResendPackets.GetPacketKeys()

	for _, seqNum := range keys {
		packetInfo, ok := c.ResendPackets.GetSentPacket(seqNum)
		if !ok {
			continue // Skip this packet if it's not found
		}

		if c.config.Debug && packetInfo.Data.chunk != nil {
			packetInfo.Data.chunk.AddCallStack("Connection.ResendLostPacket")
		}
		if packetInfo.ResendCount < c.config.MaxResendCount {
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
					log.Printf("One Packet resent with SEQ %d and payload length %d!\n", packetInfo.Data.SequenceNumber-c.InitialSeq, len(packetInfo.Data.Payload))
					c.Params.OutputChan <- packetInfo.Data
					// Update resend information
					c.ResendPackets.UpdateSentPacket(seqNum)
				}
			}
		} else {
			// Mark the packet for removal if it has been resent too many times
			packetsToRemove = append(packetsToRemove, seqNum)
		}

		if c.config.Debug && packetInfo.Data.chunk != nil {
			packetInfo.Data.chunk.PopCallStack()
		}
	}

	// Remove the marked packets outside of the loop
	for _, seqNum := range packetsToRemove {
		c.ResendPackets.RemoveSentPacket(seqNum)
	}
}

// Method to start the resend timer
func (c *Connection) StartResendTimer() {
	// Start the keepalive timer
	c.resendTimer = time.AfterFunc(c.ResendInterval, func() {
		c.resendTimerMutex.Lock()
		defer c.resendTimerMutex.Unlock()
		if c.resendTimer != nil {
			c.resendTimer.Stop()
			c.resendLostPacket()
			c.StartResendTimer()
		}
	})
}

func (c *Connection) sendKeepalivePacket() {
	// Create and send the keepalive packet
	keepalivePacket := NewPcpPacket(c.NextSequenceNumber-1, c.LastAckNumber, ACKFlag, []byte{0}, c)

	if c.config.Debug && keepalivePacket.chunk != nil {
		keepalivePacket.chunk.AddCallStack("connection.sendKeepalivePacket")
	}

	keepalivePacket.IsKeepAliveMassege = true
	c.Params.OutputChan <- keepalivePacket
	fmt.Println("Sending keepalive packet...")
	if c.config.Debug && keepalivePacket.chunk != nil {
		keepalivePacket.chunk.PopCallStack()
		keepalivePacket.chunk.AddToChannel("c.OutputChan")
	}
}

func (c *Connection) startKeepaliveTimer() {
	// Lock to ensure exclusive access to the timer
	c.KeepaliveTimerMutex.Lock()
	defer c.KeepaliveTimerMutex.Unlock()

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
		if c.TimeoutCount == c.config.MaxKeepaliveAttempts {
			log.Printf("Connection %s idle timed out. Close it.\n", c.Params.Key)
			// connection idle timed out. clear connection resoureces and close connection
			c.ClearConnResource()

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
		lastACKNum = SeqIncrementBy(lastACKNum, uint32(payloadLength))

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
	newRightEdge := SeqIncrementBy(receivedSEQ, uint32(payloadLength))

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
	if c.config.Debug && packet.chunk != nil {
		packet.chunk.AddCallStack("c.UpdateResendPacketsOnAck")
	}
	// Sort SACK blocks by LeftEdge
	sort.Slice(c.TcpOptions.InSACKOption.Blocks, func(i, j int) bool {
		return isLess(c.TcpOptions.InSACKOption.Blocks[i].LeftEdge, c.TcpOptions.InSACKOption.Blocks[j].LeftEdge)
	})

	// Create a list to store resend packet's SEQ in ascending order
	var seqToRemove []uint32

	c.ResendPackets.RemovalLock()
	defer c.ResendPackets.RemovalUnlock()

	// Get a collection of packet keys for iteration
	keys := c.ResendPackets.GetPacketKeys()

	// Iterate through ResendPackets
	for _, seqNum := range keys {
		_, ok := c.ResendPackets.GetSentPacket(seqNum)
		if !ok {
			continue // Skip this packet if it's not found
		}
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

	if c.config.Debug && packet.chunk != nil {
		packet.chunk.PopCallStack()
	}
}

func (c *Connection) InitSendSyn() {
	synPacket := NewPcpPacket(c.InitialSeq, 0, SYNFlag, nil, c)
	synPacket.IsOpenConnection = false
	c.Params.SigOutputChan <- synPacket
	c.InitClientState = SynSent
}

func (c *Connection) InitSendSynAck() {
	synAckPacket := NewPcpPacket(c.InitialSeq, SeqIncrement(c.InitialPeerSeq), SYNFlag|ACKFlag, nil, c)
	synAckPacket.IsOpenConnection = false
	// Send the SYN-ACK packet to the sender
	c.Params.SigOutputChan <- synAckPacket
	c.InitServerState = SynAckSent // set 3-way handshake state
}

func (c *Connection) InitSendAck() {
	ackPacket := NewPcpPacket(SeqIncrement(c.InitialSeq), SeqIncrement(c.InitialPeerSeq), ACKFlag, nil, c)
	ackPacket.IsOpenConnection = false
	c.Params.SigOutputChan <- ackPacket
	c.InitClientState = AckSent
}

func (c *Connection) TermCallerSendFin() {
	// Assemble FIN packet to be sent to the other end
	finPacket := NewPcpPacket(c.TermStartSeq, c.TermStartPeerSeq, FINFlag|ACKFlag, nil, c)
	if c.Params.SigOutputChan != nil {
		c.Params.SigOutputChan <- finPacket
		// set 4-way termination state to CallerFINSent
		c.TerminationCallerState = CallerFinSent
	}
}

func (c *Connection) TermCallerSendAck() {
	ackPacket := NewPcpPacket(c.TermStartSeq+1, c.TermStartPeerSeq+1, ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	if c.Params.SigOutputChan != nil {
		c.Params.SigOutputChan <- ackPacket
		// set 4-way termination state to CallerFINSent
		c.TerminationCallerState = CallerAckSent
	}
}

func (c *Connection) TermRespSendFinAck() {
	ackPacket := NewPcpPacket(c.TermStartSeq, c.TermStartPeerSeq, FINFlag|ACKFlag, nil, c)
	// Send the acknowledgment packet to the other end
	if c.Params.SigOutputChan != nil {
		c.Params.SigOutputChan <- ackPacket
		c.TerminationRespState = RespFinAckSent
	}
}

// start Connection Signal timer to resend signal messages in 3-way handshake and 4-way termination
func (c *Connection) StartConnSignalTimer() {
	if c.ConnSignalTimer != nil {
		c.ConnSignalTimer.Stop()
	}

	// Restart the timer
	c.ConnSignalTimer = time.AfterFunc(time.Second*time.Duration(c.config.ConnSignalRetryInterval), func() {
		// Increment ConnSignalRetryCount
		c.ConnSignalRetryCount++

		// Check if retries exceed the maximum allowed retries
		if c.ConnSignalRetryCount >= c.config.ConnSignalRetry {
			// Signal 3-way handshake or 4-way termination failure
			c.ConnSignalTimer.Stop()
			c.ConnSignalRetryCount = 0 // reset it to 0
			close(c.ConnSignalFailed)  // send failure signal to other function
			return
		}

		// Determine the appropriate packet to resend based on the connection state
		switch {
		case c.InitClientState == SynSent:
			// Resend SYN packet
			c.InitSendSyn()
		case c.InitServerState == SynAckSent:
			// Resend SYN-ACK packet
			c.InitSendSynAck()
		case c.TerminationCallerState == CallerFinSent:
			// Resend FIN packet from caller side
			c.TermCallerSendFin()
		case c.TerminationRespState == RespFinAckSent:
			// Resend ACK and FIN packet from responder side
			c.TermRespSendFinAck()
		}

		c.StartConnSignalTimer()
	})
}

func (c *Connection) StopConnSignalTimer() {
	c.ConnSignalTimer.Stop()
}
