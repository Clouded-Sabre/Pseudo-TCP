package config

import (
	"fmt"
	"log"
	"os"

	"sync"

	"gopkg.in/yaml.v2"
)

var Mu sync.Mutex

type Config struct {
	ServerIP                string `yaml:"server_ip"`
	ServerPort              int    `yaml:"server_port"`
	ClientIP                string `yaml:"client_ip"`
	ClientPortLower         int    `yaml:"client_port_lower"`
	ClientPortUpper         int    `yaml:"client_port_upper"`
	ProtocolID              int    `yaml:"protocol_id"`
	PreferredMSS            int    `yaml:"preferred_mss"`
	WindowScale             int    `yaml:"window_scale"`
	WindowSizeWithScale     int    `yaml:"window_size_with_scale"`
	KeepaliveInterval       int    `yaml:"keepalive_interval"`
	IdleTimeout             int    `yaml:"idle_timeout"`
	MaxKeepaliveAttempts    int    `yaml:"max_keepalive_attempts"`
	KeepAliveEnabled        bool   `yaml:"keep_alive_enabled"`
	PacketLostSimulation    bool   `yaml:"packet_lost_simulation"`
	MaxResendCount          int    `yaml:"max_resend_count"` // max resend tries
	ResendInterval          int    `yaml:"resend_interval"`  // packet resend interval in number of mini-seconds
	SackPermitSupport       bool   `yaml:"sack_permit_support"`
	SackOptionSupport       bool   `yaml:"sack_option_support"`
	PayloadPoolSize         int    `yaml:"payload_pool_size"`
	ConnSignalRetry         int    `yaml:"conn_signal_retry"`
	ConnSignalRetryInterval int    `yaml:"conn_signal_retry_interval"`
}

var AppConfig *Config

func ReadConfig() (*Config, error) {
	// Read the YAML file
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("error reading YAML file: %v", err)
	}

	// Unmarshal YAML data into Config struct
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatalf("error unmarshalling YAML data: %v", err)
	}

	// Print the configuration
	fmt.Printf("%+v\n", config)

	return &config, nil
}
