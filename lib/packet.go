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

	rp "github.com/Clouded-Sabre/ringpool/lib"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
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

	// Create a TCP layer
	tcp := &layers.TCP{
		SrcPort: layers.TCPPort(p.SourcePort),
		DstPort: layers.TCPPort(p.DestinationPort),
		Seq:     p.SequenceNumber,
		Ack:     p.AcknowledgmentNum,
		Window:  p.WindowSize,
		SYN:     p.Flags&0x02 != 0,
		ACK:     p.Flags&0x10 != 0,
		FIN:     p.Flags&0x01 != 0,
		RST:     p.Flags&0x04 != 0,
		PSH:     p.Flags&0x08 != 0,
		URG:     p.Flags&0x20 != 0,
		ECE:     p.Flags&0x40 != 0,
		CWR:     p.Flags&0x80 != 0,
		Urgent:  p.UrgentPointer,
		Options: []layers.TCPOption{},
	}

	// Add TCP options
	if !p.IsOpenConnection && p.TcpOptions.windowScaleShiftCount > 0 {
		tcp.Options = append(tcp.Options, layers.TCPOption{
			OptionType:   layers.TCPOptionKindWindowScale,
			OptionLength: 3,
			OptionData:   []byte{p.TcpOptions.windowScaleShiftCount},
		})
	}
	if !p.IsOpenConnection && p.TcpOptions.mss > 0 {
		tcp.Options = append(tcp.Options, layers.TCPOption{
			OptionType:   layers.TCPOptionKindMSS,
			OptionLength: 4,
			OptionData:   []byte{byte(p.TcpOptions.mss >> 8), byte(p.TcpOptions.mss & 0xff)},
		})
	}
	if !p.IsOpenConnection && p.TcpOptions.permitSack {
		tcp.Options = append(tcp.Options, layers.TCPOption{
			OptionType:   layers.TCPOptionKindSACKPermitted,
			OptionLength: 2,
		})
	}
	if p.IsOpenConnection && p.TcpOptions.SackEnabled {
		for _, block := range p.TcpOptions.outSACKOption.blocks {
			tcp.Options = append(tcp.Options, layers.TCPOption{
				OptionType:   layers.TCPOptionKindSACK,
				OptionLength: 8,
				OptionData:   append([]byte{byte(block.leftEdge >> 24), byte(block.leftEdge >> 16), byte(block.leftEdge >> 8), byte(block.leftEdge)}, []byte{byte(block.rightEdge >> 24), byte(block.rightEdge >> 16), byte(block.rightEdge >> 8), byte(block.rightEdge)}...),
			})
		}
	}
	if p.TcpOptions.timestampEnabled {
		timestamp := time.Now().UnixMicro()
		tcp.Options = append(tcp.Options, layers.TCPOption{
			OptionType:   layers.TCPOptionKindTimestamps,
			OptionLength: 10,
			OptionData:   append([]byte{byte(timestamp >> 24), byte(timestamp >> 16), byte(timestamp >> 8), byte(timestamp)}, []byte{byte(p.TcpOptions.tsEchoReplyValue >> 24), byte(p.TcpOptions.tsEchoReplyValue >> 16), byte(p.TcpOptions.tsEchoReplyValue >> 8), byte(p.TcpOptions.tsEchoReplyValue)}...),
		})
	}

	// Serialize the TCP layer
	serializeBuffer := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	err := tcp.SerializeTo(serializeBuffer, opts)
	if err != nil {
		return 0, err
	}

	// Copy the serialized data to the provided buffer
	copy(buffer, serializeBuffer.Bytes())

	if rp.Debug && p.chunk != nil {
		p.chunk.TickFootPrint(fp)
	}
	return len(serializeBuffer.Bytes()), nil
}

// Unmarshal converts a byte slice to a PcpPacket
func (p *PcpPacket) Unmarshal(data []byte, srcAddr, destAddr net.Addr) error {
	var fp int
	if rp.Debug && p.GetChunkReference() != nil {
		fp = p.AddFootPrint("p.Unmarshal")
	}

	packet := gopacket.NewPacket(data, layers.LayerTypeTCP, gopacket.Default)
	tcpLayer := packet.Layer(layers.LayerTypeTCP)
	if tcpLayer == nil {
		return fmt.Errorf("failed to decode TCP layer")
	}
	tcp, _ := tcpLayer.(*layers.TCP)

	p.SrcAddr = srcAddr
	p.DestAddr = destAddr
	p.SourcePort = uint16(tcp.SrcPort)
	p.DestinationPort = uint16(tcp.DstPort)
	p.SequenceNumber = tcp.Seq
	p.AcknowledgmentNum = tcp.Ack
	p.Flags = 0
	if tcp.SYN {
		p.Flags |= 0x02
	}
	if tcp.ACK {
		p.Flags |= 0x10
	}
	if tcp.FIN {
		p.Flags |= 0x01
	}
	if tcp.RST {
		p.Flags |= 0x04
	}
	if tcp.PSH {
		p.Flags |= 0x08
	}
	if tcp.URG {
		p.Flags |= 0x20
	}
	if tcp.ECE {
		p.Flags |= 0x40
	}
	if tcp.CWR {
		p.Flags |= 0x80
	}
	p.WindowSize = tcp.Window
	p.UrgentPointer = tcp.Urgent

	// Parse TCP options
	if p.TcpOptions == nil {
		p.TcpOptions = &options{}
	}
	for _, option := range tcp.Options {
		switch option.OptionType {
		case layers.TCPOptionKindWindowScale:
			if len(option.OptionData) == 1 {
				p.TcpOptions.windowScaleShiftCount = option.OptionData[0]
			}
		case layers.TCPOptionKindMSS:
			if len(option.OptionData) == 2 {
				p.TcpOptions.mss = binary.BigEndian.Uint16(option.OptionData)
			}
		case layers.TCPOptionKindSACKPermitted:
			p.TcpOptions.permitSack = true
		case layers.TCPOptionKindSACK:
			for i := 0; i < len(option.OptionData); i += 8 {
				leftEdge := binary.BigEndian.Uint32(option.OptionData[i : i+4])
				rightEdge := binary.BigEndian.Uint32(option.OptionData[i+4 : i+8])
				p.TcpOptions.inSACKOption.blocks = append(p.TcpOptions.inSACKOption.blocks, sackblock{leftEdge: leftEdge, rightEdge: rightEdge})
			}
		case layers.TCPOptionKindTimestamps:
			if len(option.OptionData) == 8 {
				p.TcpOptions.timestampEnabled = true
				p.TcpOptions.timestamp = binary.BigEndian.Uint32(option.OptionData[:4])
				p.TcpOptions.tsEchoReplyValue = binary.BigEndian.Uint32(option.OptionData[4:])
			}
		}
	}

	// Extract payload
	p.Payload = tcp.Payload

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
