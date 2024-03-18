package lib

import (
	"encoding/binary"
	"net"
)

const (
	ProtocolID = 20 // my custom IP protocol number
	HeaderSize = 20 // HeaderSize is the size of the header in bytes (assumes option field is not used). Adjust as per your header size
)

// CustomPacket represents a packet in your custom protocol
type CustomPacket struct {
	SourcePort        uint16 // SourcePort represents the source port
	DestinationPort   uint16 // DestinationPort represents the destination port
	SequenceNumber    uint32 // SequenceNumber represents the sequence number
	AcknowledgmentNum uint32 // AcknowledgmentNum represents the acknowledgment number
	//DataLength        uint16 // DataLength represents the length of the payload data
	WindowSize    uint16 // WindowSize specifies the number of bytes the receiver is willing to receive
	Flags         uint8  // Flags represent various control flags
	UrgentPointer uint16 // UrgentPointer indicates the end of the urgent data (empty for now)
	Options       []byte // Options represent TCP options (empty for now)
	Checksum      uint16 // Checksum is the checksum of the packet
	Payload       []byte // Payload represents the payload data
}

// Marshal converts a CustomPacket to a byte slice
// Marshal converts a CustomPacket to a byte slice
func (p *CustomPacket) Marshal(srcAddr, dstAddr net.Addr) []byte {
	// Calculate the length of the options field (including padding)
	optionsLength := len(p.Options)
	padding := 0
	if optionsLength%4 != 0 {
		padding = 4 - (optionsLength % 4)
	}
	totalLength := HeaderSize + optionsLength + padding

	// Allocate space for the frame
	frame := make([]byte, totalLength)

	// Write header fields
	binary.BigEndian.PutUint16(frame[0:2], p.SourcePort)
	binary.BigEndian.PutUint16(frame[2:4], p.DestinationPort)
	binary.BigEndian.PutUint32(frame[4:8], p.SequenceNumber)
	binary.BigEndian.PutUint32(frame[8:12], p.AcknowledgmentNum)

	// Calculate Data Offset and Reserved field (DO and RSV)
	doAndRsv := uint8(HeaderSize/4) << 4

	// Write DO and RSV into the 12th byte
	frame[12] = doAndRsv

	// Write Flags into the 13th byte
	frame[13] = p.Flags

	binary.BigEndian.PutUint16(frame[14:16], p.WindowSize)
	// leave frame[16:18] as all zero for now
	binary.BigEndian.PutUint16(frame[18:20], p.UrgentPointer)

	// Copy options into frame
	copy(frame[20:20+optionsLength], p.Options)

	pcpFrameLength := totalLength + len(p.Payload)
	// Calculate checksum over the pseudo-header, TCP header, and payload
	pseudoHeader := assemblePseudoHeader(srcAddr, dstAddr, uint16(pcpFrameLength))
	data := append(pseudoHeader, append(frame, p.Payload...)...)
	checksum := CalculateChecksum(data)
	binary.BigEndian.PutUint16(frame[16:18], checksum)

	// Append payload to the frame
	frame = append(frame, p.Payload...)

	return frame
}

// Unmarshal converts a byte slice to a CustomPacket
func (p *CustomPacket) Unmarshal(data []byte) {
	p.SourcePort = binary.BigEndian.Uint16(data[0:2])
	p.DestinationPort = binary.BigEndian.Uint16(data[2:4])
	p.SequenceNumber = binary.BigEndian.Uint32(data[4:8])
	p.AcknowledgmentNum = binary.BigEndian.Uint32(data[8:12])
	p.Flags = data[13] // Updated to byte 13 for Flags
	p.WindowSize = binary.BigEndian.Uint16(data[14:16])
	p.UrgentPointer = binary.BigEndian.Uint16(data[18:20])

	// Calculate the Data Offset (DO) to determine the options length
	do := (data[12] >> 4) * 4
	optionsLength := int(do) - HeaderSize

	// Extract options from the data
	p.Options = make([]byte, optionsLength)
	copy(p.Options, data[HeaderSize:HeaderSize+optionsLength])

	// Extract payload from the data
	p.Payload = data[HeaderSize+optionsLength:]

	// Retrieve the checksum from the packet
	p.Checksum = binary.BigEndian.Uint16(data[16:18]) // Assuming checksum field is at byte 16 and 17
}

func NewCustomPacket(sourcePort, destinationPort uint16, seqNum, ackNum uint32, flags uint8, data []byte) *CustomPacket {
	return &CustomPacket{
		SourcePort:        sourcePort,
		DestinationPort:   destinationPort,
		SequenceNumber:    seqNum,
		AcknowledgmentNum: ackNum,
		Flags:             flags,
		Payload:           data,
	}
}

// PacketVector represents a vector containing packet data along with metadata.
type PacketVector struct {
	Data                  *CustomPacket // Packet data
	RemoteAddr, LocalAddr net.Addr      // Source address of the packet
}

func CalculateChecksum(frame []byte) uint16 {
	// Initialize sum to zero (32-bit for potential overflow handling)
	sum := uint32(0)

	// Process 16-bit words (2 bytes each)
	for i := 0; i < len(frame); i += 2 {
		// Extract 16-bit word from frame and convert to uint32
		word := uint32(binary.BigEndian.Uint16(frame[i : i+2]))
		// Add the word to the sum
		sum += word
	}

	// Handle potential carry from addition (one's complement addition)
	for sum>>16 > 0 {
		// Isolate the carry bit and add it to the lower 16 bits (wrap around)
		sum = (sum & 0xffff) + (sum >> 16)
	}

	// Take one's complement of the final sum
	checksum := ^uint16(sum)

	return checksum
}

func VerifyChecksum(data []byte, srcAddr, dstAddr net.Addr) bool {
	// Retrieve the checksum from the packet
	receivedChecksum := binary.BigEndian.Uint16(data[16:18]) // Assuming checksum field is at byte 16 and 17

	// Zero out the checksum field in data for calculation
	binary.BigEndian.PutUint16(data[16:18], 0)

	// Calculate checksum over the pseudo-header, TCP header, and payload
	pcpFrameLength := uint16(len(data))
	pseudoHeader := assemblePseudoHeader(srcAddr, dstAddr, pcpFrameLength)
	checksumData := append(pseudoHeader, data...)
	// Calculate the checksum
	calculatedChecksum := CalculateChecksum(checksumData)

	// Restore the original checksum field in data
	binary.BigEndian.PutUint16(data[16:18], receivedChecksum)

	// Compare the received checksum with the calculated checksum
	return receivedChecksum == calculatedChecksum
}

// assemblePseudoHeader assembles the pseudo-header for checksum calculation
func assemblePseudoHeader(srcAddr, dstAddr net.Addr, pcpFrameLength uint16) []byte {
	pseudoHeader := make([]byte, 12)
	srcIP := srcAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	dstIP := dstAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	binary.BigEndian.PutUint32(pseudoHeader[0:4], binary.BigEndian.Uint32(srcIP))
	binary.BigEndian.PutUint32(pseudoHeader[4:8], binary.BigEndian.Uint32(dstIP))
	// leave byte 8 (Fixed 8 bits) as all zero as byte 8
	pseudoHeader[9] = uint8(ProtocolID)
	binary.BigEndian.PutUint16(pseudoHeader[10:12], pcpFrameLength)
	return pseudoHeader
}
