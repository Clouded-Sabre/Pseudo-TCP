package lib

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
)

const (
	HeaderSize = 20 // HeaderSize is the size of the header in bytes (assumes option field is not used). Adjust as per your header size
)

// PcpPacket represents a packet in your custom protocol
type PcpPacket struct {
	SrcAddr, DestAddr net.Addr
	SourcePort        uint16 // SourcePort represents the source port
	DestinationPort   uint16 // DestinationPort represents the destination port
	SequenceNumber    uint32 // SequenceNumber represents the sequence number
	AcknowledgmentNum uint32 // AcknowledgmentNum represents the acknowledgment number
	WindowSize        uint16 // WindowSize specifies the number of bytes the receiver is willing to receive
	Flags             uint8  // Flags represent various control flags
	UrgentPointer     uint16 // UrgentPointer indicates the end of the urgent data (empty for now)
	Checksum          uint16 // Checksum is the checksum of the packet
	Payload           []byte // Payload represents the payload data
	TcpOptions        *Options
	IsOpenConnection  bool // only used in outgoing packet to denote if the connection is open or in 3-way handshake stage
}

// Marshal converts a PcpPacket to a byte slice
func (p *PcpPacket) Marshal(protocolId uint8) []byte {
	// Calculate the length of the options field (including padding)
	optionsLength := 0
	optionsPresent := false
	if !p.IsOpenConnection && p.TcpOptions.WindowScaleShiftCount > 0 { // Window Scaling is enabled
		// Window scaling option: kind (1 byte), length (1 byte), shift count (1 byte)
		optionsLength += 3
		optionsPresent = true
	}
	if !p.IsOpenConnection && p.TcpOptions.MSS > 0 { // MSS is enabled
		// MSS option: kind (1 byte), length (1 byte), MSS value (2 bytes)
		optionsLength += 4
		optionsPresent = true
	}
	if !p.IsOpenConnection && p.TcpOptions.SupportSack {
		// MSS option: kind (1 byte), length (1 byte)
		optionsLength += 2
		optionsPresent = true
	}

	padding := 0
	if optionsPresent && optionsLength%4 != 0 {
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
	doAndRsv := uint8(totalLength/4) << 4

	// Write DO and RSV into the 12th byte
	frame[12] = doAndRsv

	// Write Flags into the 13th byte
	frame[13] = p.Flags

	binary.BigEndian.PutUint16(frame[14:16], p.WindowSize)
	// leave frame[16:18] as all zero for now
	binary.BigEndian.PutUint16(frame[18:20], p.UrgentPointer)

	// Construct options
	optionOffset := HeaderSize
	if !p.IsOpenConnection && p.TcpOptions.WindowScaleShiftCount > 0 {
		// Window scaling option: kind (1 byte), length (1 byte), shift count (1 byte)
		frame[optionOffset] = 3   // Kind: Window Scale
		frame[optionOffset+1] = 3 // Length: 3 bytes
		frame[optionOffset+2] = p.TcpOptions.WindowScaleShiftCount
		optionOffset += 3
	}
	if !p.IsOpenConnection && p.TcpOptions.MSS > 0 {
		// MSS option: kind (1 byte), length (1 byte), MSS value (2 bytes)
		frame[optionOffset] = 2   // Kind: Maximum Segment Size
		frame[optionOffset+1] = 4 // Length: 4 bytes
		binary.BigEndian.PutUint16(frame[optionOffset+2:optionOffset+4], p.TcpOptions.MSS)
		optionOffset += 4
	}
	if !p.IsOpenConnection && p.TcpOptions.SupportSack {
		// MSS option: kind (1 byte), length (1 byte)
		frame[optionOffset] = 4   // Kind: SACK permitted
		frame[optionOffset+1] = 2 // Length: 2 bytes
		optionOffset += 2
	}

	// Append padding if necessary
	if optionsPresent {
		for i := 0; i < padding; i++ {
			frame[optionOffset+i] = 1 // NOP option
		}
	}

	pcpFrameLength := totalLength + len(p.Payload)
	// Calculate checksum over the pseudo-header, TCP header, and payload
	pseudoHeader := assemblePseudoHeader(p.SrcAddr, p.DestAddr, protocolId, uint16(pcpFrameLength))
	data := append(pseudoHeader, append(frame, p.Payload...)...)
	checksum := CalculateChecksum(data)
	binary.BigEndian.PutUint16(frame[16:18], checksum)

	// Append payload to the frame
	frame = append(frame, p.Payload...)

	return frame
}

// Unmarshal converts a byte slice to a PcpPacket
func (p *PcpPacket) Unmarshal(data []byte, srcAddr, destAddr net.Addr) {
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
	optionsLength := int(do) - HeaderSize

	// Extract options from the data
	options := data[HeaderSize : HeaderSize+optionsLength]

	if p.TcpOptions == nil {
		p.TcpOptions = &Options{} // all attributes set to zero for which means disabled
	}
	// Parse options to extract Window Scale and MSS values
	var (
		optionLength, optionKind byte
	)
	for i := 0; i < optionsLength-1; {
		optionKind = options[i]
		//fmt.Println("Scan to option kind", optionKind)

		if optionKind == 0 {
			break // padding reached
		} else {
			switch optionKind {
			case 1: // no op
				optionLength = 1
			case 3: // Window Scale
				optionLength = options[i+1]
				if optionLength == 3 && i+2 < optionsLength {
					p.TcpOptions.WindowScaleShiftCount = options[i+2]
				}
			case 2: // Maximum Segment Size (MSS)
				optionLength = options[i+1]
				if optionLength == 4 && i+4 < optionsLength {
					p.TcpOptions.MSS = binary.BigEndian.Uint16(options[i+2 : i+4])
				}
			case 4: // SACK support
				optionLength = options[i+1]
				if optionLength == 2 {
					p.TcpOptions.SupportSack = true
				}
			default:
				optionLength = options[i+1]
			}
			// Move to the next option
			i += int(optionLength)
		}
	}

	// Extract payload from the data
	p.Payload = data[HeaderSize+optionsLength:]

	// Retrieve the checksum from the packet
	p.Checksum = binary.BigEndian.Uint16(data[16:18]) // Assuming checksum field is at byte 16 and 17
}

func NewPcpPacket(seqNum, ackNum uint32, flags uint8, data []byte, conn *Connection) *PcpPacket {
	return &PcpPacket{
		SrcAddr:           conn.LocalAddr,
		DestAddr:          conn.RemoteAddr,
		SourcePort:        uint16(conn.LocalPort),
		DestinationPort:   uint16(conn.RemotePort),
		SequenceNumber:    seqNum,
		AcknowledgmentNum: ackNum,
		Flags:             flags,
		WindowSize:        conn.WindowSize,
		Payload:           data,
		IsOpenConnection:  conn.IsOpenConnection,
		TcpOptions:        conn.TcpOptions,
	}
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
	// Retrieve the checksum from the packet
	receivedChecksum := binary.BigEndian.Uint16(data[16:18]) // Assuming checksum field is at byte 16 and 17

	// Zero out the checksum field in data for calculation
	binary.BigEndian.PutUint16(data[16:18], 0)

	// Calculate checksum over the pseudo-header, TCP header, and payload
	pcpFrameLength := uint16(len(data))
	pseudoHeader := assemblePseudoHeader(srcAddr, dstAddr, protocolId, pcpFrameLength)
	checksumData := append(pseudoHeader, data...)
	// Calculate the checksum
	calculatedChecksum := CalculateChecksum(checksumData)

	// Restore the original checksum field in data
	binary.BigEndian.PutUint16(data[16:18], receivedChecksum)
	//log.Printf(Red+"Calculated Checksum: %x, Extracted Checksum: %x"+Reset, calculatedChecksum, receivedChecksum)

	// Compare the received checksum with the calculated checksum
	return receivedChecksum == calculatedChecksum
}

// assemblePseudoHeader assembles the pseudo-header for checksum calculation
func assemblePseudoHeader(srcAddr, dstAddr net.Addr, protocolId uint8, pcpFrameLength uint16) []byte {
	pseudoHeader := make([]byte, 12)
	srcIP := srcAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	dstIP := dstAddr.(*net.IPAddr).IP.To4() // Type assertion to get the IPv4 address
	binary.BigEndian.PutUint32(pseudoHeader[0:4], binary.BigEndian.Uint32(srcIP))
	binary.BigEndian.PutUint32(pseudoHeader[4:8], binary.BigEndian.Uint32(dstIP))
	// leave byte 8 (Fixed 8 bits) as all zero as byte 8
	pseudoHeader[9] = protocolId
	binary.BigEndian.PutUint16(pseudoHeader[10:12], pcpFrameLength)
	return pseudoHeader
}

// Function to extract payload from an IP packet
func ExtractIpPayload(ipFrame []byte) ([]byte, error) {
	// Check if the minimum size of the IP header is present
	if len(ipFrame) < 20 {
		return nil, fmt.Errorf("invalid IP packet: insufficient header length")
	}

	// Determine the length of the IP header (in 32-bit words)
	headerLen := int(ipFrame[0]&0x0F) * 4

	// Check if the packet length is valid
	if len(ipFrame) < headerLen {
		return nil, fmt.Errorf("invalid IP packet: insufficient packet length")
	}

	// Extract the payload by skipping past the IP header
	payload := ipFrame[headerLen:]

	return payload, nil
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
