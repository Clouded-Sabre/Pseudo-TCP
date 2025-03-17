package config

import (
	"fmt"
	"os"

	"github.com/Clouded-Sabre/Pseudo-TCP/lib"
	"gopkg.in/yaml.v3"
)

var counter int

// Partial configuration structs with pointer fields.
type PartialPcpCoreConfig struct {
	ProtocolID           *int  `yaml:"protocol_id"`
	PayloadPoolSize      *int  `yaml:"payload_pool_size"`
	PreferredMSS         *int  `yaml:"preferred_mss"`
	Debug                *bool `yaml:"debug"`
	PoolDebug            *bool `yaml:"pool_debug"`
	ProcessTimeThreshold *int  `yaml:"processing_time_threshold"`
}

type PartialPcpProtocolConnConfig struct {
	IptableRuleDelay *int `yaml:"iptable_rule_daley"`
	//PreferredMSS     *int  `yaml:"preferred_mss"`
	PacketLostSim    *bool `yaml:"packet_lost_simulation"`
	PConnTimeout     *int  `yaml:"pconn_time_out"`
	ClientPortUpper  *int  `yaml:"client_port_upper"`
	ClientPortLower  *int  `yaml:"client_port_lower"`
	VerifyChecksum   *bool `yaml:"checksum_verification"`
	PconnOutputQueue *int  `yaml:"pconn_output_queue"`
}

type PartialConnectionConfig struct {
	WindowScale *int `yaml:"window_scale"`
	//PreferredMSS            *int  `yaml:"preferred_mss"`
	SackPermitSupport       *bool `yaml:"sack_permit_support"`
	SackOptionSupport       *bool `yaml:"sack_option_support"`
	IdleTimeout             *int  `yaml:"idle_timeout"`
	KeepAliveEnabled        *bool `yaml:"keep_alive_enabled"`
	KeepaliveInterval       *int  `yaml:"keepalive_interval"`
	MaxKeepaliveAttempts    *int  `yaml:"max_keepalive_attempts"`
	ResendInterval          *int  `yaml:"resend_interval"`
	MaxResendCount          *int  `yaml:"max_resend_count"`
	WindowSizeWithScale     *int  `yaml:"window_size_with_scale"`
	ConnSignalRetryInterval *int  `yaml:"conn_signal_retry_interval"`
	ConnSignalRetry         *int  `yaml:"conn_signal_retry"`
	ConnectionInputQueue    *int  `yaml:"connection_input_queue"`
	ShowStatistics          *bool `yaml:"show_statistics"`
}

// PartialConfig groups the partial configs together.
type PartialConfig struct {
	CoreConfig *PartialPcpCoreConfig         `yaml:",inline"`
	PcpConfig  *PartialPcpProtocolConnConfig `yaml:",inline"`
	ConnConfig *PartialConnectionConfig      `yaml:",inline"`
}

// Merge functions update default values if a partial value is provided.
func mergePcpCoreConfig(defaultConfig *lib.PcpCoreConfig, partial *PartialPcpCoreConfig) {
	if partial == nil {
		return
	}
	if partial.ProtocolID != nil {
		counter++
		defaultConfig.ProtocolID = uint8(*partial.ProtocolID)
	}
	if partial.PayloadPoolSize != nil {
		counter++
		defaultConfig.PayloadPoolSize = *partial.PayloadPoolSize
	}
	if partial.PreferredMSS != nil {
		counter++
		defaultConfig.PreferredMSS = *partial.PreferredMSS
	}
	if partial.Debug != nil {
		counter++
		defaultConfig.Debug = *partial.Debug
	}
	if partial.PoolDebug != nil {
		counter++
		defaultConfig.PoolDebug = *partial.PoolDebug
	}
	if partial.ProcessTimeThreshold != nil {
		counter++
		defaultConfig.ProcessTimeThreshold = *partial.ProcessTimeThreshold
	}
}

func mergePcpProtocolConnConfig(defaultConfig *lib.PcpProtocolConnConfig, partial *PartialPcpProtocolConnConfig) {
	if partial == nil {
		return
	}
	if partial.IptableRuleDelay != nil {
		counter++
		defaultConfig.IptableRuleDaley = *partial.IptableRuleDelay
	}
	if partial.PacketLostSim != nil {
		counter++
		defaultConfig.PacketLostSimulation = *partial.PacketLostSim
	}
	if partial.PConnTimeout != nil {
		counter++
		defaultConfig.PConnTimeout = *partial.PConnTimeout
	}
	if partial.ClientPortUpper != nil {
		counter++
		defaultConfig.ClientPortUpper = *partial.ClientPortUpper
	}
	if partial.ClientPortLower != nil {
		counter++
		defaultConfig.ClientPortLower = *partial.ClientPortLower
	}
	if partial.VerifyChecksum != nil {
		defaultConfig.VerifyChecksum = *partial.VerifyChecksum
	}
	if partial.PconnOutputQueue != nil {
		counter++
		defaultConfig.PConnOutputQueue = *partial.PconnOutputQueue
	}
}

func mergeConnectionConfig(defaultConfig *lib.ConnectionConfig, partial *PartialConnectionConfig) {
	if partial == nil {
		return
	}
	if partial.WindowScale != nil {
		counter++
		defaultConfig.WindowScale = *partial.WindowScale
	}
	if partial.SackPermitSupport != nil {
		counter++
		defaultConfig.SackPermitSupport = *partial.SackPermitSupport
	}
	if partial.SackOptionSupport != nil {
		counter++
		defaultConfig.SackOptionSupport = *partial.SackOptionSupport
	}
	if partial.IdleTimeout != nil {
		counter++
		defaultConfig.IdleTimeout = *partial.IdleTimeout
	}
	if partial.KeepAliveEnabled != nil {
		counter++
		defaultConfig.KeepAliveEnabled = *partial.KeepAliveEnabled
	}
	if partial.KeepaliveInterval != nil {
		counter++
		defaultConfig.KeepaliveInterval = *partial.KeepaliveInterval
	}
	if partial.MaxKeepaliveAttempts != nil {
		counter++
		defaultConfig.MaxKeepaliveAttempts = *partial.MaxKeepaliveAttempts
	}
	if partial.ResendInterval != nil {
		counter++
		defaultConfig.ResendInterval = *partial.ResendInterval
	}
	if partial.MaxResendCount != nil {
		counter++
		defaultConfig.MaxResendCount = *partial.MaxResendCount
	}
	if partial.WindowSizeWithScale != nil {
		counter++
		defaultConfig.WindowSizeWithScale = *partial.WindowSizeWithScale
	}
	if partial.ConnSignalRetryInterval != nil {
		counter++
		defaultConfig.ConnSignalRetryInterval = *partial.ConnSignalRetryInterval
	}
	if partial.ConnSignalRetry != nil {
		counter++
		defaultConfig.ConnSignalRetry = *partial.ConnSignalRetry
	}
	if partial.ConnectionInputQueue != nil {
		counter++
		defaultConfig.ConnectionInputQueue = *partial.ConnectionInputQueue
	}
	if partial.ShowStatistics != nil {
		counter++
		defaultConfig.ShowStatistics = *partial.ShowStatistics
	}
}

// LoadConfig reads the file and merges provided values with defaults.
func LoadConfig(filename string) (*lib.PcpCoreConfig, *lib.ConnectionConfig, error) {
	coreConfig := lib.NewPcpCoreConfig()        // default config
	pcpConfig := lib.NewPcpProtocolConnConfig() // default config
	connConfig := lib.NewConnectionConfig()     // default config

	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, return defaults.
			coreConfig.PcpProtocolConnConfig = pcpConfig
			return coreConfig, connConfig, nil
		}
		return nil, nil, err
	}

	var partialCfg PartialConfig
	if err := yaml.Unmarshal(data, &partialCfg); err != nil {
		return nil, nil, err
	}

	mergePcpCoreConfig(coreConfig, partialCfg.CoreConfig)
	mergePcpProtocolConnConfig(pcpConfig, partialCfg.PcpConfig)
	mergeConnectionConfig(connConfig, partialCfg.ConnConfig)

	coreConfig.PcpProtocolConnConfig = pcpConfig

	// PreferredMSS is a shared field, so we need to update it manually.
	if partialCfg.CoreConfig != nil && partialCfg.CoreConfig.PreferredMSS != nil {
		pcpConfig.PreferredMSS = *partialCfg.CoreConfig.PreferredMSS
		connConfig.PreferredMSS = *partialCfg.CoreConfig.PreferredMSS
	}

	fmt.Println("Config item counter found:", counter)

	return coreConfig, connConfig, nil
}

/*func main() {
	coreConfig, connConfig, err := LoadConfig("config.yaml")
	if err != nil {
		fmt.Println("Error loading config:", err)
		return
	}
	fmt.Printf("Core Config: %+v\n", coreConfig)
	fmt.Printf("PCP Config: %+v\n", coreConfig.PcpProtocolConnConfig)
	fmt.Printf("Connection Config: %+v\n", connConfig)
}*/
