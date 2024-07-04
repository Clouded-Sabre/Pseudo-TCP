package lib

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	//"github.com/Clouded-Sabre/Pseudo-TCP/config"
	rp "github.com/Clouded-Sabre/ringpool/lib"
)

// PcpPacket represents a packet in your custom protocol
type PcpPacket struct {
	SrcAddr, DestAddr  net.Addr
	SourcePort         uint16 // SourcePort represents the source port
	DestinationPort    uint16 // DestinationPort represents the destination port
	SequenceNumber     uint32 // SequenceNumber represents the sequence number
	AcknowledgmentNum  uint32 // AcknowledgmentNum represents the acknowledgment number
	WindowSize         uint16 // WindowSize specifies the number of bytes the receiver is willing to receive
	Flags              uint8  // Flags represent various control flags
	UrgentPointer      uint16 // UrgentPointer indicates the end of the urgent data (empty for now)
	Checksum           uint16 // Checksum is the checksum of the packet
	Payload            []byte // Payload represents the payload data
	TcpOptions         *options
	IsOpenConnection   bool        // only used in outgoing packet to denote if the connection is open or in 3-way handshake stage
	Conn               *Connection // used for outgoing packets only to denote which connection it belongs to
	chunk              *rp.Element // point to memory chunk used to store payload
	IsKeepAliveMassege bool        // denote if this is a keepalive massage. If yes, don't put it into Connection's ResendPackets
}

// Marshal converts a PcpPacket to a byte slice
func (p *PcpPacket) Marshal(protocolId uint8, buffer []byte) (int, error) {
	var fp int
	if rp.Debug && p.GetChunkReference() != nil {
		fp = p.AddFootPrint("p.Marshal")
	}
	// Calculate the length of the options field (including padding)
	optionsLength := 0
	optionsPresent := false
	if !p.IsOpenConnection && p.TcpOptions.windowScaleShiftCount > 0 { // Window Scaling is enabled
		// Window scaling option: kind (1 byte), length (1 byte), shift count (1 byte)
		optionsLength += 3
		optionsPresent = true
	}
	if !p.IsOpenConnection && p.TcpOptions.mss > 0 { // MSS is enabled
		// MSS option: kind (1 byte), length (1 byte), MSS value (2 bytes)
		optionsLength += 4
		optionsPresent = true
	}
	if !p.IsOpenConnection && p.TcpOptions.permitSack {
		// MSS option: kind (1 byte), length (1 byte)
		optionsLength += 2
		optionsPresent = true
	}
	if p.IsOpenConnection && p.TcpOptions.SackEnabled {
		if len(p.TcpOptions.outSACKOption.blocks) > 0 {
			// SACK option kind 5: kind (1 byte), length (1 byte), SACK blocks
			optionsLength += 2 + len(p.TcpOptions.outSACKOption.blocks)*8 // 8 bytes per SACK block
			optionsPresent = true
		}
	}
	if p.TcpOptions.timestampEnabled {
		// Timestamp option: kind (1 byte), length (1 byte), timestamp value (4 bytes), echo reply value (4 bytes)
		optionsLength += 10
		optionsPresent = true
	}

	if optionsPresent && optionsLength > TcpOptionsMaxLength {
		optionsLength = TcpOptionsMaxLength // TCP option's max length is 40
	}

	padding := 0
	if optionsPresent && optionsLength%4 != 0 {
		padding = 4 - (optionsLength % 4)
	}
	totalHeaderLength := TcpHeaderLength + optionsLength + padding

	pcpFrameLength := totalHeaderLength + len(p.Payload)
	if pcpFrameLength+TcpPseudoHeaderLength > len(buffer) {
		err := fmt.Errorf("buffer size (%d) is too small to hold the frame (%d) + TcpPseudoHeader", len(buffer), pcpFrameLength)
		return 0, err
	}

	// Allocate space for the frame
	//frame := make([]byte, totalHeaderLength)
	frame := buffer[TcpPseudoHeaderLength:]

	// Write header fields
	binary.BigEndian.PutUint16(frame[0:2], p.SourcePort)
	binary.BigEndian.PutUint16(frame[2:4], p.DestinationPort)
	binary.BigEndian.PutUint32(frame[4:8], p.SequenceNumber)
	binary.BigEndian.PutUint32(frame[8:12], p.AcknowledgmentNum)

	// Calculate Data Offset and Reserved field (DO and RSV)
	doAndRsv := uint8(totalHeaderLength/4) << 4

	// Write DO and RSV into the 12th byte
	frame[12] = doAndRsv

	// Write Flags into the 13th byte
	frame[13] = p.Flags

	binary.BigEndian.PutUint16(frame[14:16], p.WindowSize)
	// leave frame[16:18] (checksum) as all zero for now
	binary.BigEndian.PutUint16(frame[16:18], 0)
	binary.BigEndian.PutUint16(frame[18:20], p.UrgentPointer)

	// Construct options
	optionOffset := TcpHeaderLength
	if !p.IsOpenConnection && p.TcpOptions.windowScaleShiftCount > 0 {
		// Window scaling option: kind (1 byte), length (1 byte), shift count (1 byte)
		frame[optionOffset] = 3   // Kind: Window Scale
		frame[optionOffset+1] = 3 // Length: 3 bytes
		frame[optionOffset+2] = p.TcpOptions.windowScaleShiftCount
		optionOffset += 3
	}
	if !p.IsOpenConnection && p.TcpOptions.mss > 0 {
		// MSS option: kind (1 byte), length (1 byte), MSS value (2 bytes)
		frame[optionOffset] = 2   // Kind: Maximum Segment Size
		frame[optionOffset+1] = 4 // Length: 4 bytes
		binary.BigEndian.PutUint16(frame[optionOffset+2:optionOffset+4], p.TcpOptions.mss)
		optionOffset += 4
	}
	if !p.IsOpenConnection && p.TcpOptions.permitSack {
		// SACK permit option: kind (1 byte), length (1 byte)
		frame[optionOffset] = 4   // Kind: SACK permitted
		frame[optionOffset+1] = 2 // Length: 2 bytes
		optionOffset += 2
	}
	if p.IsOpenConnection && p.TcpOptions.SackEnabled {
		if len(p.TcpOptions.outSACKOption.blocks) > 0 {
			// SACK option kind 5: kind (1 byte), length (1 byte), SACK blocks
			frame[optionOffset] = 5                                                    // Kind: SACK
			frame[optionOffset+1] = byte(2 + len(p.TcpOptions.outSACKOption.blocks)*8) // Length: variable
			optionOffset += 2
			// Write SACK blocks
			for _, block := range p.TcpOptions.outSACKOption.blocks {
				if optionOffset+8 >= TcpHeaderLength+TcpOptionsMaxLength {
					break
				}
				binary.BigEndian.PutUint32(frame[optionOffset:optionOffset+4], block.leftEdge)
				binary.BigEndian.PutUint32(frame[optionOffset+4:optionOffset+8], block.rightEdge)
				optionOffset += 8
			}
		}
	}
	if p.TcpOptions.timestampEnabled {
		if optionOffset+10 < TcpHeaderLength+TcpOptionsMaxLength {
			// Timestamp option: kind (1 byte), length (1 byte), timestamp value (4 bytes), echo reply value (4 bytes)
			frame[optionOffset] = 8    // Kind: Timestamp
			frame[optionOffset+1] = 10 // Length: 10 bytes (timestamp value + echo reply value)
			// Write timestamp value (current time) and echo reply value
			timestamp := time.Now().UnixMicro()
			echoReplyValue := p.TcpOptions.tsEchoReplyValue
			binary.BigEndian.PutUint32(frame[optionOffset+2:optionOffset+6], uint32(timestamp))
			binary.BigEndian.PutUint32(frame[optionOffset+6:optionOffset+10], uint32(echoReplyValue))
			optionOffset += 10
		}
	}

	// Append padding if necessary
	if optionsPresent {
		for i := 0; i < padding; i++ {
			frame[optionOffset+i] = 1 // NOP option
		}
	}

	// Calculate checksum over the pseudo-header, TCP header, and payload
	err := assemblePseudoHeader(buffer[:TcpPseudoHeaderLength], p.SrcAddr, p.DestAddr, protocolId, uint16(pcpFrameLength))
	if err != nil {
		log.Fatal(err)
	}
	// Append payload to the frame
	if len(p.Payload) > 0 {
		copy(frame[totalHeaderLength:], p.Payload)
	}

	checksum := CalculateChecksum(buffer[:TcpPseudoHeaderLength+pcpFrameLength])
	binary.BigEndian.PutUint16(frame[16:18], checksum)

	if rp.Debug && p.chunk != nil {
		p.chunk.TickFootPrint(fp)
	}
	return pcpFrameLength, nil
}

// Unmarshal converts a byte slice to a PcpPacket
func (p *PcpPacket) Unmarshal(data []byte, srcAddr, destAddr net.Addr) error {
	var fp int
	if rp.Debug && p.GetChunkReference() != nil {
		fp = p.AddFootPrint("p.Unmarshal")
	}
	if len(data) < TcpHeaderLength {
		return fmt.Errorf("the length(%d) of data is too short to be unmarshalled", len(data))
	}
	p.SrcAddr = srcAddr
	p.DestAddr = destAddr
	p.SourcePort = binary.BigEndian.Uint16(data[0:2])
	p.DestinationPort = binary.BigEndian.Uint16(data[2:4])
	p.SequenceNumber = binary.BigEndian.Uint32(data[4:8])
	p.AcknowledgmentNum = binary.BigEndian.Uint32(data[8:12])
	p.Flags = data[13] // Updated to byte 13 for Flags
	p.WindowSize = binary.BigEndian.Uint16(data[14:16])
	p.UrgentPointer = binary.BigEndian.Uint16(data[18:20])

	// Calculate the Data Offset (DO) to determine the options length
	do := (data[12] >> 4) * 4
	optionsLength := int(do) - TcpHeaderLength
	if optionsLength < 0 {
		return fmt.Errorf("packet unmarshall: The length(%d) of option is less than 0", optionsLength)
	}

	// Extract options from the data
	optionsBytes := data[TcpHeaderLength : TcpHeaderLength+optionsLength]

	if p.TcpOptions == nil {
		p.TcpOptions = &options{} // all attributes set to zero for which means disabled
	}
	// Parse options to extract Window Scale and MSS values
	var (
		optionLength, optionKind byte
	)
	for i := 0; i < optionsLength-1; {
		optionKind = optionsBytes[i]
		//fmt.Println("Scan to option kind", optionKind)

		if optionKind == 0 {
			break // padding reached
		} else {
			switch optionKind {
			case 1: // no op
				optionLength = 1
			case 3: // Window Scale
				optionLength = optionsBytes[i+1]
				if optionLength == 3 && i+3 <= optionsLength {
					p.TcpOptions.windowScaleShiftCount = optionsBytes[i+2]
				}
			case 2: // Maximum Segment Size (MSS)
				optionLength = optionsBytes[i+1]
				if optionLength == 4 && i+4 <= optionsLength {
					p.TcpOptions.mss = binary.BigEndian.Uint16(optionsBytes[i+2 : i+4])
				}
			case 4: // SACK support
				optionLength = optionsBytes[i+1]
				if optionLength == 2 {
					p.TcpOptions.permitSack = true
				}
			case 5: // SACK option
				optionLength = optionsBytes[i+1]
				if optionLength > 2 && i+int(optionLength) <= optionsLength {
					// Parse SACK blocks
					sackBlocks := make([]sackblock, 0)
					for j := i + 2; j < i+int(optionLength); j += 8 {
						leftEdge := binary.BigEndian.Uint32(optionsBytes[j : j+4])
						rightEdge := binary.BigEndian.Uint32(optionsBytes[j+4 : j+8])
						sackBlocks = append(sackBlocks, sackblock{leftEdge: leftEdge, rightEdge: rightEdge})
					}
					if p.TcpOptions.inSACKOption.blocks == nil {
						p.TcpOptions.inSACKOption.blocks = make([]sackblock, 0)
					}
					p.TcpOptions.inSACKOption.blocks = append(p.TcpOptions.inSACKOption.blocks, sackBlocks...)
				}
			case 8: // Timestamp option
				optionLength = optionsBytes[i+1]
				if optionLength == 10 && i+10 <= optionsLength {
					// Extract timestamp value and echo reply value
					timestamp := binary.BigEndian.Uint32(optionsBytes[i+2 : i+6])
					echoReplyValue := binary.BigEndian.Uint32(optionsBytes[i+6 : i+10])
					p.TcpOptions.timestampEnabled = true
					p.TcpOptions.timestamp = timestamp
					p.TcpOptions.tsEchoReplyValue = echoReplyValue
					//log.Printf("Got TSval:%d and TsErv:%d", timestamp, echoReplyValue)
				}
			default:
				optionLength = optionsBytes[i+1]
			}
			// Move to the next option
			i += int(optionLength)
		}
	}

	// Extract payload from the data
	if TcpHeaderLength+optionsLength > len(data) {
		return fmt.Errorf("TcpHeaderLength+optionsLength(%d) > len(data)(%d). Malformed PCP packet", TcpHeaderLength+optionsLength, len(data))
	}
	if len(data[TcpHeaderLength+optionsLength:]) > 0 {
		err := p.CopyToPayload(data[TcpHeaderLength+optionsLength:])
		if err != nil {
			return fmt.Errorf("packet unmarshal: error copying packet payload - %s", err)
		}
	} else {
		p.Payload = nil
	}
	//p.Payload = data[HeaderSize+optionsLength:]

	// Retrieve the checksum from the packet
	p.Checksum = binary.BigEndian.Uint16(data[16:18]) // Assuming checksum field is at byte 16 and 17

	if rp.Debug && p.GetChunkReference() != nil {
		p.TickFootPrint(fp)
	}

	return nil
}

func NewPcpPacket(seqNum, ackNum uint32, flags uint8, data []byte, conn *Connection) *PcpPacket {
	// Create a copy of the Options struct
	/*var tcpOptionsCopy *Options
	if conn.TcpOptions != nil {
		tcpOptionsCopy = &Options{
			WindowScaleShiftCount: conn.TcpOptions.WindowScaleShiftCount,
			MSS:                   conn.TcpOptions.MSS,
			SupportSack:           conn.TcpOptions.SupportSack,
			TimestampEnabled:      conn.TcpOptions.TimestampEnabled,
			TsEchoReplyValue:      conn.TcpOptions.TsEchoReplyValue,
			Timestamp:             conn.TcpOptions.Timestamp,
		}
	}*/
	newPacket := &PcpPacket{
		SrcAddr:           conn.params.localAddr,
		DestAddr:          conn.params.remoteAddr,
		SourcePort:        uint16(conn.params.localPort),
		DestinationPort:   uint16(conn.params.remotePort),
		SequenceNumber:    seqNum,
		AcknowledgmentNum: ackNum,
		Flags:             flags,
		WindowSize:        conn.windowSize,
		//Payload:           data,
		IsOpenConnection: conn.isOpenConnection,
		TcpOptions:       conn.tcpOptions,
		//TcpOptions:        tcpOptionsCopy,
		Conn: conn,
	}
	if len(data) > 0 {
		err := newPacket.CopyToPayload(data)
		if err != nil {
			log.Println("newPcpPacket error:", err)
			return nil
		}
	}
	return newPacket
}

func (p *PcpPacket) Duplicate() (*PcpPacket, error) {
	dPacket := NewPcpPacket(p.SequenceNumber, p.AcknowledgmentNum, p.Flags, p.chunk.Data.(*Payload).GetSlice(), p.Conn)
	if dPacket == nil {
		return nil, fmt.Errorf("failed to duplicate packet")
	}

	return dPacket, nil
}

func (p *PcpPacket) CopyToPayload(src []byte) error {
	if len(src) == 0 {
		err := fmt.Errorf("p.CopyToPayload: Source slice is empty")
		log.Println(err)
		return err
	}
	p.GetChunk()
	if p.chunk == nil {
		err := fmt.Errorf("p.CopyToPayload: Got an nil chunk")
		log.Println(err)
		return err
	}
	err := p.chunk.Data.(*Payload).Copy(src)
	if err != nil {
		p.ReturnChunk()
		return fmt.Errorf("PcpPacket.CopyToPayload: %s", err)
	}
	p.Payload = p.chunk.Data.(*Payload).GetSlice()
	return nil
}

func (p *PcpPacket) ReturnChunk() {
	if p.chunk != nil {
		Pool.ReturnElement(p.chunk)
		p.chunk = nil
	}
}

func (p *PcpPacket) GetChunk() {
	p.chunk = Pool.GetElement()
}

func (p *PcpPacket) GetChunkReference() *rp.Element {
	return p.chunk
}

func (p *PcpPacket) AddFootPrint(fpStr string) int {
	return p.chunk.AddFootPrint(fpStr)
}

func (p *PcpPacket) TickFootPrint(fp int) {
	p.chunk.TickFootPrint(fp)
}

func (p *PcpPacket) AddChannel(chanStr string) {
	p.chunk.AddChannel(chanStr)
}

func (p *PcpPacket) TickChannel() error {
	return p.chunk.TickChannel()
}

func (p *PcpPacket) GetPayloadLength() int {
	return len(p.chunk.Data.(*Payload).GetSlice())
}

func CalculateChecksum(buffer []byte) uint16 {
	var cksum uint32 = 0

	// Process 16-bit words (2 bytes each)
	for i := 0; i < len(buffer)-1; i += 2 {
		word := binary.BigEndian.Uint16(buffer[i : i+2])
		cksum += uint32(word)
	}

	// Handle remaining odd byte, if any
	if len(buffer)%2 != 0 {
		cksum += uint32(buffer[len(buffer)-1]) << 8 // Shift last byte to 16 bits
	}

	// Fold 32-bit sum to 16 bits
	cksum = (cksum >> 16) + (cksum & 0xffff)
	cksum += (cksum >> 16)

	// Return one's complement of the final sum
	return ^uint16(cksum)
}

func VerifyChecksum(data []byte, srcAddr, dstAddr net.Addr, protocolId uint8) bool {
	// Please note that the first TcpPseudoHeaderLength bytes of data is reserved for TCP Pseudo header
	if len(data) < TcpHeaderLength+TcpPseudoHeaderLength {
		log.Printf("The received packet's total length is too short(%d)\n", len(data))
		return false
	}
	frame := data[TcpPseudoHeaderLength:]
	// Retrieve the checksum from the packet
	receivedChecksum := binary.BigEndian.Uint16(frame[16:18]) // Assuming checksum field is at byte 16 and 17

	// Zero out the checksum field in data for calculation
	binary.BigEndian.PutUint16(frame[16:18], 0)

	// Calculate checksum over the pseudo-header, TCP header, and payload
	pcpFrameLength := uint16(len(frame))
	err := assemblePseudoHeader(data[:TcpPseudoHeaderLength], srcAddr, dstAddr, protocolId, pcpFrameLength)
	if err != nil {
		log.Println("error in assembling pseudo tcp header:", err)
		return false
	}
	//checksumData := append(pseudoHeader, data...)
	// Calculate the checksum
	calculatedChecksum := CalculateChecksum(data)

	// Restore the original checksum field in data
	binary.BigEndian.PutUint16(frame[16:18], receivedChecksum)
	//log.Printf(Red+"Calculated Checksum: %x, Extracted Checksum: %x"+Reset, calculatedChecksum, receivedChecksum)

	// Compare the received checksum with the calculated checksum
	return receivedChecksum == calculatedChecksum
}

// assemblePseudoHeader assembles the pseudo-header for checksum calculation
func assemblePseudoHeader(buffer []byte, srcAddr, dstAddr net.Addr, protocolId uint8, pcpFrameLength uint16) error {
	if len(buffer) != TcpPseudoHeaderLength {
		return fmt.Errorf("tcp pseudo header Buffer length(%d) is not TcpPseudoHeaderLength", len(buffer))
	}
	srcIP := srcAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	dstIP := dstAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	binary.BigEndian.PutUint32(buffer[0:4], binary.BigEndian.Uint32(srcIP))
	binary.BigEndian.PutUint32(buffer[4:8], binary.BigEndian.Uint32(dstIP))
	// leave byte 8 (Fixed 8 bits) as all zero as byte 8
	buffer[8] = 0
	buffer[9] = protocolId
	binary.BigEndian.PutUint16(buffer[10:12], pcpFrameLength)
	return nil
}

// Function to extract payload from an IP packet
func ExtractIpPayload(ipFrame []byte) (int, error) {
	// Check if the minimum size of the IP header is present
	if len(ipFrame) < 20 {
		return 0, fmt.Errorf("invalid IP packet: insufficient header length")
	}

	// Determine the length of the IP header (in 32-bit words)
	headerLen := int(ipFrame[0]&0x0F) * 4

	// Check if the packet length is valid
	if len(ipFrame) < headerLen {
		return 0, fmt.Errorf("invalid IP packet: insufficient packet length")
	}

	// Extract the payload by skipping past the IP header
	//payload := ipFrame[headerLen:]

	return headerLen, nil
}

func GenerateISN() (uint32, error) {
	// Generate a random 32-bit value
	var isn uint32
	err := binary.Read(rand.Reader, binary.BigEndian, &isn)
	if err != nil {
		return 0, err
	}
	return isn, nil
}

// PacketInfo represents information about a sent packet
type PacketInfo struct {
	LastSentTime time.Time // Time the packet was last sent
	ResendCount  int       // Number of times the packet has been resent
	Data         *PcpPacket
}

type ResendPackets struct {
	mutex, removalMutex sync.Mutex
	packets             map[uint32]PacketInfo
}

func NewResendPackets() *ResendPackets {
	return &ResendPackets{
		packets: make(map[uint32]PacketInfo),
	}
}

func (r *ResendPackets) RemovalLock() {
	r.removalMutex.Lock()
}

func (r *ResendPackets) RemovalUnlock() {
	r.removalMutex.Unlock()
}

// Function to add a sent packet to the map
func (r *ResendPackets) AddSentPacket(packet *PcpPacket) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.packets[packet.SequenceNumber] = PacketInfo{
		LastSentTime: time.Now(),
		ResendCount:  0, // Initial resend count is 1
		Data:         packet,
	}
}

// Function to update information about a sent packet
func (r *ResendPackets) GetSentPacket(seqNum uint32) (*PacketInfo, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if packetInfo, ok := r.packets[seqNum]; ok {
		return &packetInfo, true
	} else {
		return nil, false
	}
}

// Function to update information about a sent packet
func (r *ResendPackets) UpdateSentPacket(seqNum uint32) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if packetInfo, ok := r.packets[seqNum]; ok {
		packetInfo.LastSentTime = time.Now()
		packetInfo.ResendCount++
		// Update other relevant fields as needed
		r.packets[seqNum] = packetInfo
		return nil
	} else {
		err := fmt.Errorf("corresponding packet not found")
		return err
	}
}

// Function to remove a sent packet from the map
func (r *ResendPackets) RemoveSentPacket(seqNum uint32) {
	var fp int

	r.mutex.Lock()
	defer r.mutex.Unlock()
	packet, ok := r.packets[seqNum]
	if !ok {
		log.Println("RemoveSentPackact error: No such packets with SEQ", seqNum)
		return
	}
	if rp.Debug && packet.Data.GetChunkReference() != nil {
		fp = packet.Data.AddFootPrint("ResendPackets.RemoveSentPacket")
	}

	delete(r.packets, seqNum)
	// now that we delete packet from SentPackets, we no longer
	// need it so it's time to return its chunk
	if rp.Debug && packet.Data.GetChunkReference() != nil {
		packet.Data.TickFootPrint(fp)
	}
	packet.Data.ReturnChunk()
}

func (r *ResendPackets) GetPacketKeys() []uint32 {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	keys := make([]uint32, 0, len(r.packets))
	for key := range r.packets {
		keys = append(keys, key)
	}
	return keys
}

type PacketGapMap struct {
	mutex   sync.Mutex
	packets map[uint32]*ReceivedPacket // key is SEQ
}

type ReceivedPacket struct {
	ReceivedTime time.Time // Time the packet was last sent
	Packet       *PcpPacket
}

func NewReceivedPacket(packet *PcpPacket) *ReceivedPacket {
	return &ReceivedPacket{
		ReceivedTime: time.Now(),
		Packet:       packet,
	}
}

func NewPacketGapMap() *PacketGapMap {
	return &PacketGapMap{
		packets: make(map[uint32]*ReceivedPacket),
	}
}

func (pgm *PacketGapMap) AddPacket(packet *PcpPacket) {
	pgm.mutex.Lock()
	defer pgm.mutex.Unlock()
	rp := NewReceivedPacket(packet)
	pgm.packets[packet.SequenceNumber] = rp
}

func (pgm *PacketGapMap) RemovePacket(seqNum uint32) {
	pgm.mutex.Lock()
	defer pgm.mutex.Unlock()
	_, ok := pgm.packets[seqNum]
	if !ok {
		return
	}
	delete(pgm.packets, seqNum)
}

func (pgm *PacketGapMap) GetPacket(seqNum uint32) (*ReceivedPacket, bool) {
	pgm.mutex.Lock()
	defer pgm.mutex.Unlock()
	packet, found := pgm.packets[seqNum]
	return packet, found
}

// Method to retrieve packets from PacketGapMap in ascending order by SEQ
func (pgm *PacketGapMap) getPacketsInAscendingOrder() []*ReceivedPacket {
	pgm.mutex.Lock()
	defer pgm.mutex.Unlock()

	// Create a slice to store packets
	packets := make([]*ReceivedPacket, 0, len(pgm.packets))
	for _, packet := range pgm.packets {
		packets = append(packets, packet)
	}

	// Sort the packets in ascending order by SEQ
	sort.Slice(packets, func(i, j int) bool {
		return packets[i].Packet.SequenceNumber < packets[j].Packet.SequenceNumber
	})

	return packets
}
