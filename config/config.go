package config

import (
	"encoding/json"
	"log"
	"os"

	"sync"

	"gopkg.in/yaml.v2"
)

var Mu sync.Mutex

var configDebug = true

type Config struct {
	ClientPortLower         int  `yaml:"client_port_lower"`
	ClientPortUpper         int  `yaml:"client_port_upper"`
	ProtocolID              int  `yaml:"protocol_id"`
	PreferredMSS            int  `yaml:"preferred_mss"`
	WindowScale             int  `yaml:"window_scale"`
	WindowSizeWithScale     int  `yaml:"window_size_with_scale"`
	KeepaliveInterval       int  `yaml:"keepalive_interval"`
	IdleTimeout             int  `yaml:"idle_timeout"`
	MaxKeepaliveAttempts    int  `yaml:"max_keepalive_attempts"`
	KeepAliveEnabled        bool `yaml:"keep_alive_enabled"`
	PacketLostSimulation    bool `yaml:"packet_lost_simulation"`
	MaxResendCount          int  `yaml:"max_resend_count"` // max resend tries
	ResendInterval          int  `yaml:"resend_interval"`  // packet resend interval in number of mini-seconds
	SackPermitSupport       bool `yaml:"sack_permit_support"`
	SackOptionSupport       bool `yaml:"sack_option_support"`
	PayloadPoolSize         int  `yaml:"payload_pool_size"`
	ConnSignalRetry         int  `yaml:"conn_signal_retry"`
	ConnSignalRetryInterval int  `yaml:"conn_signal_retry_interval"`
	PConnTimeout            int  `yaml:"pconn_time_out"`
	IptableRuleDaley        int  `yaml:"iptable_rule_daley"`
	//ChecksumVerification    bool `yaml:"checksum_verification"`
	Debug                   bool `yaml:"debug"`
	PoolDebug               bool `yaml:"pool_debug"`
	ProcessingTimeThreshold int  `yaml:"processing_time_threshold"`
	ConnectionInputQueue    int  `yaml:"connection_input_queue"`
	PconnOutputQueue        int  `yaml:"pconn_output_queue"`
	ShowStatistics          bool `yaml:"show_statistics"`
}

var AppConfig *Config

func ReadConfig(confFilePath string) (*Config, error) {
	// Initialize default config values
	config := Config{
		ClientPortLower:         32768, // Linux system convention for client local port range
		ClientPortUpper:         60999, // Linux system convention for client local port range
		ProtocolID:              6,     // tcp prtocol id
		PreferredMSS:            1412,
		WindowScale:             14,
		WindowSizeWithScale:     131072, //128K
		KeepaliveInterval:       5,      // seconds
		IdleTimeout:             1800,   // 30 minutes
		MaxKeepaliveAttempts:    3,      // max keepalive attemps
		KeepAliveEnabled:        false,
		PacketLostSimulation:    false,
		MaxResendCount:          5,    // max number of resend before failed
		ResendInterval:          1000, // interval between resend of lost packets
		SackPermitSupport:       true,
		SackOptionSupport:       false,
		PayloadPoolSize:         2000, // number of packets in pre-allocated payload pool
		ConnSignalRetry:         5,    // 3-way handshake and 4-way termination max number of retry
		ConnSignalRetryInterval: 2,    // in seconds
		PConnTimeout:            10,   // in seconds
		IptableRuleDaley:        200,  // in milliseconds
		//ChecksumVerification:    false, // default is false due to hardware assisted checksum verification is typically used
		Debug:                   false,
		PoolDebug:               false,
		ProcessingTimeThreshold: 50,   // used in packet ring pool to check if a function or channel holds a packet for too long time
		ConnectionInputQueue:    1000, // PCP connection's InputQueue depth
		PconnOutputQueue:        1000, // PCP Protocol connection's OutputQueue depth
		ShowStatistics:          false,
	}

	// Read the YAML file
	data, err := os.ReadFile(confFilePath)
	if err != nil {
		log.Fatalf("error reading YAML file: %v", err)
	}

	// Unmarshal YAML data into Config struct
	//var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		log.Fatalf("error unmarshalling YAML data: %v", err)
	}

	// Print the configuration
	if configDebug {
		confJSON, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return nil, err
		}
		log.Printf("PCP Configuration: %s\n", string(confJSON))
	}

	return &config, nil
}
