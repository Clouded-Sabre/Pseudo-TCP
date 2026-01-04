package lib

import (
	"log"
	"os/exec"
)

// PacketFilterer provides an abstraction for firewall rule management.
// It supports both iptables and nftables, with automatic detection and fallback.
type PacketFilterer interface {
	// AddRule adds a rule to drop RST packets.
	// direction: "client" (destination-based) or "server" (source-based)
	// Returns nil if the rule was successfully added or already exists.
	AddRule(ip string, port int, direction string) error

	// RemoveRule removes a previously added rule.
	// Returns nil if the rule was successfully removed or doesn't exist.
	RemoveRule(ip string, port int, direction string) error
}

// NewPacketFilterer creates a PacketFilterer instance.
// It automatically detects available firewall tools in this order:
// 1. nftables (nft command)
// 2. iptables (iptables command)
// 3. No-op implementation (if neither is available)
func NewPacketFilterer() PacketFilterer {
	if isNftablesAvailable() {
		log.Println("Using nftables for packet filtering")
		return NewNftablesFilterer()
	} else if isIptablesAvailable() {
		log.Println("Using iptables for packet filtering")
		return NewIptablesFilterer()
	} else {
		log.Println("WARNING: Neither nftables nor iptables found; packet filtering disabled")
		return NewNoOpFilterer()
	}
}

// isNftablesAvailable checks if the nft command is available.
func isNftablesAvailable() bool {
	cmd := exec.Command("which", "nft")
	return cmd.Run() == nil
}

// isIptablesAvailable checks if the iptables command is available.
func isIptablesAvailable() bool {
	cmd := exec.Command("which", "iptables")
	return cmd.Run() == nil
}

// NoOpFilterer is a no-op implementation when no firewall tool is available.
type NoOpFilterer struct{}

func NewNoOpFilterer() *NoOpFilterer {
	return &NoOpFilterer{}
}

func (n *NoOpFilterer) AddRule(ip string, port int, direction string) error {
	return nil
}

func (n *NoOpFilterer) RemoveRule(ip string, port int, direction string) error {
	return nil
}
