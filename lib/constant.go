package lib

const (
	SynReceived          = 1 // 3-way handshake server state
	SynAckSent           = 2 // 3-way handshake server state
	AckReceived          = 3 // 3-way handshake server state
	SynSent              = 1 // 3-way handshake client state
	SynAckReceived       = 2 // 3-way handshake client state
	AckSent              = 3 // 3-way handshake client state
	CallerFinSent        = 1 // 4-way termination caller state
	CallerFinAckReceived = 2 // 4-way termination caller state
	CallerAckSent        = 3 // 4-way termination caller state
	RespFinReceived      = 1 // 4-way termination responder state
	RespFinAckSent       = 2 // 4-way termination responder state
	RespAckReceived      = 3 // 4-way termination responder state
)

// Flag constants
const (
	// PCP flag constants
	URGFlag uint8 = 1 << 5
	ACKFlag uint8 = 1 << 4
	PSHFlag uint8 = 1 << 3
	RSTFlag uint8 = 1 << 2
	SYNFlag uint8 = 1 << 1
	FINFlag uint8 = 1 << 0
)

const (
	Red   = "\033[31m"
	Reset = "\033[0m"
)

const (
	TcpOptionsMaxLength   = 40
	TcpHeaderLength       = 20 //options not included
	TcpPseudoHeaderLength = 12
	IpHeaderMaxLength     = 60
)
