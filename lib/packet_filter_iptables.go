package lib

import (
	"log"
	"os/exec"
	"strconv"
)

// IptablesFilterer implements PacketFilterer using iptables.
// It includes a duplicate rule prevention mechanism to avoid adding the same rule multiple times.
type IptablesFilterer struct{}

func NewIptablesFilterer() *IptablesFilterer {
	return &IptablesFilterer{}
}

// AddRule adds an iptables rule to drop RST packets.
// It checks if the rule already exists before adding to prevent duplicates.
func (i *IptablesFilterer) AddRule(ip string, port int, direction string) error {
	var checkCmd *exec.Cmd
	var addCmd *exec.Cmd

	if direction == "server" {
		// Server: Drop RST packets sourced from this IP:port
		// Check if rule exists
		checkCmd = exec.Command("iptables", "-C", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
		// Add rule if it doesn't exist
		addCmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
	} else {
		// Client: Drop RST packets destined for this IP:port
		// Check if rule exists
		checkCmd = exec.Command("iptables", "-C", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
		// Add rule if it doesn't exist
		addCmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
	}

	// Check if rule already exists
	if err := checkCmd.Run(); err == nil {
		// Rule already exists, nothing to do
		return nil
	}

	// Rule doesn't exist, add it
	if err := addCmd.Run(); err != nil {
		log.Printf("Failed to add iptables rule for %s:%d: %v", ip, port, err)
		return err
	}

	return nil
}

// RemoveRule removes an iptables rule.
// It doesn't check for existence first since iptables -D handles missing rules gracefully.
func (i *IptablesFilterer) RemoveRule(ip string, port int, direction string) error {
	var cmd *exec.Cmd

	if direction == "server" {
		// Server: Remove RST drop rule sourced from this IP:port
		cmd = exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-s", ip, "--sport", strconv.Itoa(port), "-j", "DROP")
	} else {
		// Client: Remove RST drop rule destined for this IP:port
		cmd = exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST",
			"-d", ip, "--dport", strconv.Itoa(port), "-j", "DROP")
	}

	// Execute the command; we ignore errors here since the rule might not exist
	// (which is fine - we wanted it deleted anyway)
	if err := cmd.Run(); err != nil {
		log.Printf("Note: iptables rule removal for %s:%d encountered: %v (may not exist)", ip, port, err)
		// Don't return error - rule removal failure is not critical
	}

	return nil
}
