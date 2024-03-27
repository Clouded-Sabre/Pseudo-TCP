package lib

import "net"

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
}

type Options struct {
	WindowScaleShiftCount uint8  // TCP Window scaling, < 14 which mean WindowSize * 2^14
	MSS                   uint16 // max tcp segment size
	SupportSack           bool   // SACK support. No real support because no retransmission happens
}
