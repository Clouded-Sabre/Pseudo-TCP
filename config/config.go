package config

import "sync"

const (
	ServerIP             = "127.0.0.2"
	ServerPort           = 7080
	ClientIP             = "127.0.0.3"
	ClientPortLower      = 32768
	ClientPortUpper      = 60999
	ProtocolID           = 6 // my custom IP protocol number
	PreferredMss         = 1440
	WindowScale          = 14
	WindowSizeWithScale  = 200 * (2 ^ 6)
	KeepaliveInterval    = 5  // TCP keepalive attempt interval in seconds
	IdleTimeout          = 25 // TCP connection idle timeout in seconds
	MaxKeepaliveAttempts = 3  // Maximum number of keepalive attempts before marking the connection as dead
	KeepAliveEnabled     = true
)

var Mu sync.Mutex
