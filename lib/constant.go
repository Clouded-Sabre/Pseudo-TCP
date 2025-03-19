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
	FINFlag = 0x01
	SYNFlag = 0x02
	RSTFlag = 0x04
	PSHFlag = 0x08
	ACKFlag = 0x10
	URGFlag = 0x20
	ECEFlag = 0x40
	CWRFlag = 0x80
)

const (
	Red   = "\033[31m"
	Reset = "\033[0m"

	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorReset   = "\033[0m"
)

const (
	TcpOptionsMaxLength   = 40
	TcpHeaderLength       = 20 //options not included
	TcpPseudoHeaderLength = 12
	IpHeaderMaxLength     = 60
)

const ( //linux convention
	minPort = 32768
	maxPort = 60999
)
