package lib

import (
	"encoding/binary"
)

const (
	ProtocolID = 20 // my custom IP protocol number
	HeaderSize = 16 // HeaderSize is the size of the header in bytes. Adjust as per your header size
)

// CustomPacket represents a packet in your custom protocol
type CustomPacket struct {
	SourcePort        uint16 // SourcePort represents the source port
	DestinationPort   uint16 // DestinationPort represents the destination port
	SequenceNumber    uint32 // SequenceNumber represents the sequence number
	AcknowledgmentNum uint32 // AcknowledgmentNum represents the acknowledgment number
	DataLength        uint16 // DataLength represents the length of the payload data
	Payload           []byte // Payload represents the payload data
}

// Marshal converts a CustomPacket to a byte slice
func (p *CustomPacket) Marshal() []byte {
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint16(header[0:2], p.SourcePort)
	binary.BigEndian.PutUint16(header[2:4], p.DestinationPort)
	binary.BigEndian.PutUint32(header[4:8], p.SequenceNumber)
	binary.BigEndian.PutUint32(header[8:12], p.AcknowledgmentNum)
	binary.BigEndian.PutUint16(header[12:14], p.DataLength)
	// Append payload to the header
	return append(header, p.Payload...)
}

// Unmarshal converts a byte slice to a CustomPacket
func (p *CustomPacket) Unmarshal(data []byte) {
	p.SourcePort = binary.BigEndian.Uint16(data[0:2])
	p.DestinationPort = binary.BigEndian.Uint16(data[2:4])
	p.SequenceNumber = binary.BigEndian.Uint32(data[4:8])
	p.AcknowledgmentNum = binary.BigEndian.Uint32(data[8:12])
	p.DataLength = binary.BigEndian.Uint16(data[12:14])
	// Extract payload from the data
	p.Payload = data[HeaderSize:]
}

func NewCustomPacket(sourcePort, destinationPort uint16, seqNum, ackNum uint32, data []byte) *CustomPacket {
	return &CustomPacket{
		SourcePort:        sourcePort,
		DestinationPort:   destinationPort,
		SequenceNumber:    seqNum,
		AcknowledgmentNum: ackNum,
		DataLength:        uint16(len(data)),
		Payload:           data,
	}
}
