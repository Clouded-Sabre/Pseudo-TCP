package shared

import (
	"encoding/binary"
)

const (
	ProtocolID = 20 // my custom IP protocol number
	HeaderSize = 12 // HeaderSize is the size of the header in bytes. Adjust as per your header size
)

// CustomPacket represents a packet in your custom protocol
type CustomPacket struct {
	ProtocolID        uint16
	SequenceNumber    uint32
	AcknowledgmentNum uint32
	DataLength        uint16
	// Add other fields as needed
	Payload []byte
}

// Marshal converts a CustomPacket to a byte slice
func (p *CustomPacket) Marshal() []byte {
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint16(header[0:2], p.ProtocolID)
	binary.BigEndian.PutUint32(header[2:6], p.SequenceNumber)
	binary.BigEndian.PutUint32(header[6:10], p.AcknowledgmentNum)
	binary.BigEndian.PutUint16(header[10:12], p.DataLength)
	// Append payload to the header
	return append(header, p.Payload...)
}

// Unmarshal converts a byte slice to a CustomPacket
func (p *CustomPacket) Unmarshal(data []byte) {
	p.ProtocolID = binary.BigEndian.Uint16(data[0:2])
	p.SequenceNumber = binary.BigEndian.Uint32(data[2:6])
	p.AcknowledgmentNum = binary.BigEndian.Uint32(data[6:10])
	p.DataLength = binary.BigEndian.Uint16(data[10:12])
	// Extract payload from the data
	p.Payload = data[HeaderSize:]
}

func NewCustomPacket(seqNum, ackNum uint32, data []byte) *CustomPacket {
	return &CustomPacket{
		ProtocolID:        ProtocolID,
		SequenceNumber:    seqNum,
		AcknowledgmentNum: ackNum,
		DataLength:        uint16(len(data)),
		Payload:           data,
	}
}
