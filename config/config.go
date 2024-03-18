package config

import "sync"

const (
	ServerIP        = "127.0.0.2"
	ServerPort      = 7080
	ClientIP        = "127.0.0.3"
	ClientPortLower = 32768
	ClientPortUpper = 60999
)

var Mu sync.Mutex
