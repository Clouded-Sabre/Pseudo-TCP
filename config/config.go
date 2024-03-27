package config

import "sync"

const (
	ServerIP            = "127.0.0.2"
	ServerPort          = 7080
	ClientIP            = "127.0.0.3"
	ClientPortLower     = 32768
	ClientPortUpper     = 60999
	ProtocolID          = 6 // my custom IP protocol number
	PreferredMss        = 65535
	WindowScale         = 14
	WindowSizeWithScale = 200 * (2 ^ 6)
)

var Mu sync.Mutex
