//go:build linux
// +build linux

package filter

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// firewallTool represents the firewall management tool to be used.
type firewallTool int

const (
	iptables firewallTool = iota
	nftables
	none
)

// filterImpl is the main implementation of the Filter interface for Linux.
// It detects the available firewall tool (iptables or nftables) and delegates
// the filtering operations to the corresponding implementation.
type filterImpl struct {
	toolSpecificFilter toolSpecificFilter
	udpServerFilter    *udpServerFilter // Struct containing the shared method
}

// toolSpecificFilter is an interface that abstracts the firewall-specific operations.
type toolSpecificFilter interface {
	AddTcpClientFiltering(dstAddr string, dstPort int) error
	RemoveTcpClientFiltering(dstAddr string, dstPort int) error
	AddTcpServerFiltering(srcAddr string, srcPort int) error
	RemoveTcpServerFiltering(srcAddr string, srcPort int) error
	AddUdpClientFiltering(dstAddr string) error
	RemoveUdpClientFiltering(dstAddr string) error
	FinishFiltering() error
}

// NewFilter creates a new Filter instance.
// It detects whether iptables or nftables is available and returns the appropriate filterer.
func NewFilter(identifier string) (Filter, error) {
	tool := detectFirewallTool()
	var toolFilter toolSpecificFilter

	switch tool {
	case nftables:
		log.Println("Using nftables for filtering.")
		toolFilter = newNftablesFilterer(identifier)
	case iptables:
		log.Println("Using iptables for filtering.")
		toolFilter = newIptablesFilterer(identifier)
	default:
		return nil, fmt.Errorf("no supported firewall tool (iptables or nftables) found")
	}

	return &filterImpl{
		toolSpecificFilter: toolFilter,
		udpServerFilter:    NewUdpServerFilter(),
	}, nil
}

// detectFirewallTool checks for the presence of nftables and iptables.
func detectFirewallTool() firewallTool {
	if _, err := exec.LookPath("nft"); err == nil {
		// Check if nftables is usable
		cmd := exec.Command("nft", "list", "tables")
		if err := cmd.Run(); err == nil {
			return nftables
		}
	}
	if _, err := exec.LookPath("iptables"); err == nil {
		cmd := exec.Command("iptables", "-S")
		if err := cmd.Run(); err == nil {
			return iptables
		}
	}
	return none
}

func (f *filterImpl) AddTcpClientFiltering(dstAddr string, dstPort int) error {
	return f.toolSpecificFilter.AddTcpClientFiltering(dstAddr, dstPort)
}

func (f *filterImpl) RemoveTcpClientFiltering(dstAddr string, dstPort int) error {
	return f.toolSpecificFilter.RemoveTcpClientFiltering(dstAddr, dstPort)
}

func (f *filterImpl) AddTcpServerFiltering(srcAddr string, srcPort int) error {
	return f.toolSpecificFilter.AddTcpServerFiltering(srcAddr, srcPort)
}

func (f *filterImpl) RemoveTcpServerFiltering(srcAddr string, srcPort int) error {
	return f.toolSpecificFilter.RemoveTcpServerFiltering(srcAddr, srcPort)
}

func (f *filterImpl) AddUdpClientFiltering(dstAddr string) error {
	fmt.Println("Now I am adding UDP client filtering rule..........................")
	return f.toolSpecificFilter.AddUdpClientFiltering(dstAddr)
}

func (f *filterImpl) RemoveUdpClientFiltering(dstAddr string) error {
	return f.toolSpecificFilter.RemoveUdpClientFiltering(dstAddr)
}

func (f *filterImpl) FinishFiltering() error {
	return f.toolSpecificFilter.FinishFiltering()
}

func (f *filterImpl) AddUdpServerFiltering(srcAddr string) error {
	return f.udpServerFilter.AddUdpServerFiltering(srcAddr)
}

func (f *filterImpl) RemoveUdpServerFiltering(srcAddr string) error {
	return f.udpServerFilter.RemoveUdpServerFiltering(srcAddr)
}

// --- iptables implementation ---

type iptablesFilterer struct {
	comment string
}

func newIptablesFilterer(identifier string) *iptablesFilterer {
	return &iptablesFilterer{comment: identifier}
}

func (f *iptablesFilterer) AddTcpClientFiltering(dstAddr string, dstPort int) error {
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -d %s --dport %d -m comment --comment \"%s\" -j DROP", dstAddr, dstPort, f.comment)
	cmd := exec.Command("iptables", "-S", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}
	if strings.Contains(string(output), ruleCheck) {
		return nil
	}
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}
	return nil
}

func (f *iptablesFilterer) RemoveTcpClientFiltering(dstAddr string, dstPort int) error {
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (f *iptablesFilterer) AddTcpServerFiltering(srcAddr string, srcPort int) error {
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -s %s --sport %d -m comment --comment %s -j DROP", srcAddr, srcPort, f.comment)
	cmd := exec.Command("iptables", "-S", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}
	if strings.Contains(string(output), ruleCheck) {
		return nil
	}
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}
	return nil
}

func (f *iptablesFilterer) RemoveTcpServerFiltering(srcAddr string, srcPort int) error {
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (f *iptablesFilterer) AddUdpClientFiltering(dstAddr string) error {
	ipStr, _, err := net.SplitHostPort(dstAddr)
	if err != nil {
		return fmt.Errorf("invalid destination address format: %v", err)
	}

	ruleCheck := fmt.Sprintf("-A OUTPUT -d %s -p icmp -m icmp --icmp-type 3/3 -j DROP", ipStr)
	cmd := exec.Command("iptables", "-S", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}
	if strings.Contains(string(output), ruleCheck) {
		return nil
	}

	cmd = exec.Command("iptables", "-A", "OUTPUT", "-d", ipStr, "-p", "icmp", "--icmp-type", "3/3", "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply iptables rule: %v", err)
	}
	return nil
}

func (f *iptablesFilterer) RemoveUdpClientFiltering(dstAddr string) error {
	ipStr, _, err := net.SplitHostPort(dstAddr)
	if err != nil {
		return fmt.Errorf("invalid destination address format: %v", err)
	}
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-d", ipStr, "-p", "icmp", "--icmp-type", "3/3", "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (f *iptablesFilterer) FinishFiltering() error {
	cmd := exec.Command("iptables", "-S")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}

	var deleteErrors []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "--comment \""+f.comment+"\"") {
			deleteCmd := strings.Replace(line, "-A", "-D", 1)
			cmd := exec.Command("sh", "-c", "iptables "+deleteCmd)
			if out, err := cmd.CombinedOutput(); err != nil {
				deleteErrors = append(deleteErrors, fmt.Sprintf("%s\nError: %s", deleteCmd, string(out)))
			}
		}
	}

	if len(deleteErrors) > 0 {
		return fmt.Errorf("some rules failed to delete:\n%s", strings.Join(deleteErrors, "\n"))
	}
	return nil
}

// --- nftables implementation ---

type nftablesFilterer struct {
	identifier string
}

func newNftablesFilterer(identifier string) *nftablesFilterer {
	return &nftablesFilterer{identifier: identifier}
}

func (n *nftablesFilterer) AddTcpClientFiltering(dstAddr string, dstPort int) error {
	return n.addRule(dstAddr, dstPort, "client")
}

func (n *nftablesFilterer) RemoveTcpClientFiltering(dstAddr string, dstPort int) error {
	return n.removeRule(dstAddr, dstPort, "client")
}

func (n *nftablesFilterer) AddTcpServerFiltering(srcAddr string, srcPort int) error {
	return n.addRule(srcAddr, srcPort, "server")
}

func (n *nftablesFilterer) RemoveTcpServerFiltering(srcAddr string, srcPort int) error {
	return n.removeRule(srcAddr, srcPort, "server")
}

func (n *nftablesFilterer) AddUdpClientFiltering(dstAddr string) error {
	fmt.Println("Now I am adding nftable udp filtering rule.......................")
	ipStr, _, err := net.SplitHostPort(dstAddr)
	if err != nil {
		return fmt.Errorf("invalid destination address format: %v", err)
	}
	rule := fmt.Sprintf("ip daddr %s icmp type destination-unreachable drop comment \"%s\"", ipStr, n.identifier)
	return n.addGenericRule(rule)
}

func (n *nftablesFilterer) RemoveUdpClientFiltering(dstAddr string) error {
	ipStr, _, err := net.SplitHostPort(dstAddr)
	if err != nil {
		return fmt.Errorf("invalid destination address format: %v", err)
	}
	rule := fmt.Sprintf("ip daddr %s icmp type destination-unreachable drop", ipStr)
	return n.removeGenericRule(rule)
}

func (n *nftablesFilterer) addGenericRule(rule string) error {
	fmt.Println("Now I am adding generic nftables rule ...........")
	if err := n.ensureTableAndChain(); err != nil {
		return err
	}

	// Check if rule exists
	cmd := exec.Command("nft", "list", "chain", "inet", "filter", "output")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list nftables rules: %w", err)
	}

	re := regexp.MustCompile(`comment ".*?"`)
	ruleCheck := re.ReplaceAllString(rule, "")
	// trim trailing spaces that might be left after removing the comment
	ruleCheck = strings.TrimSpace(ruleCheck)

	if strings.Contains(string(output), ruleCheck) {
		fmt.Println("The rule already exist!!! do nothing")
		return nil
	}

	cmd = exec.Command("nft", "add", "rule", "inet", "filter", "output", rule)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add nftables rule: %w", err)
	}
	return nil
}

func (n *nftablesFilterer) removeGenericRule(rulePattern string) error {
	handle, err := n.findRuleHandle(rulePattern)
	if err != nil {
		return err
	}
	if handle == "" {
		return nil // Rule doesn't exist
	}

	cmd := exec.Command("nft", "delete", "rule", "inet", "filter", "output", "handle", handle)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete nftables rule with handle %s: %w", handle, err)
	}
	return nil
}

func (n *nftablesFilterer) addRule(ip string, port int, direction string) error {
	if err := n.ensureTableAndChain(); err != nil {
		return err
	}

	var rule string
	var ruleCheck string
	if direction == "server" {
		rule = fmt.Sprintf("ip saddr %s tcp sport %d tcp flags rst drop comment \"%s\"", ip, port, n.identifier)
		ruleCheck = fmt.Sprintf("ip saddr %s tcp sport %d tcp flags rst drop", ip, port)
	} else {
		rule = fmt.Sprintf("ip daddr %s tcp dport %d tcp flags rst drop comment \"%s\"", ip, port, n.identifier)
		ruleCheck = fmt.Sprintf("ip daddr %s tcp dport %d tcp flags rst drop", ip, port)
	}

	// Check if rule exists
	cmd := exec.Command("nft", "list", "chain", "inet", "filter", "output")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list nftables rules: %w", err)
	}
	if strings.Contains(string(output), ruleCheck) {
		return nil
	}

	cmd = exec.Command("nft", "add", "rule", "inet", "filter", "output", rule)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add nftables rule for %s:%d: %v", ip, port, err)
	}

	return nil
}

func (n *nftablesFilterer) removeRule(ip string, port int, direction string) error {
	var rulePattern string
	if direction == "server" {
		rulePattern = fmt.Sprintf("ip saddr %s tcp sport %d tcp flags rst drop", ip, port)
	} else {
		rulePattern = fmt.Sprintf("ip daddr %s tcp dport %d tcp flags rst drop", ip, port)
	}

	handle, err := n.findRuleHandle(rulePattern)
	if err != nil {
		return fmt.Errorf("error finding rule handle: %w", err)
	}
	if handle == "" {
		// Rule doesn't exist, which is fine for removal.
		return nil
	}

	cmd := exec.Command("nft", "delete", "rule", "inet", "filter", "output", "handle", handle)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete nftables rule with handle %s: %w", handle, err)
	}
	return nil
}

func (n *nftablesFilterer) ensureTableAndChain() error {
	checkTableCmd := exec.Command("nft", "list", "table", "inet", "filter")
	if checkTableCmd.Run() != nil {
		createTableCmd := exec.Command("nft", "add", "table", "inet", "filter")
		if err := createTableCmd.Run(); err != nil {
			return fmt.Errorf("failed to create nftables table: %w", err)
		}
	}

	checkChainCmd := exec.Command("nft", "list", "chain", "inet", "filter", "output")
	if checkChainCmd.Run() != nil {
		createChainCmd := exec.Command("nft", "add", "chain", "inet", "filter", "output", "{", "type", "filter", "hook", "output", "priority", "0", ";", "}")
		if err := createChainCmd.Run(); err != nil {
			return fmt.Errorf("failed to create nftables output chain: %w", err)
		}
	}
	return nil
}

func (n *nftablesFilterer) findRuleHandle(rulePattern string) (string, error) {
	cmd := exec.Command("nft", "--handle", "list", "chain", "inet", "filter", "output")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the chain doesn't exist, the rule doesn't exist.
		if strings.Contains(string(output), "No such file or directory") {
			return "", nil
		}
		return "", fmt.Errorf("failed to list nftables rules with handles: %w, output: %s", err, string(output))
	}

	re := regexp.MustCompile(fmt.Sprintf(`.*%s.* # handle (\\d+)`, regexp.QuoteMeta(rulePattern)))
	matches := re.FindStringSubmatch(string(output))

	if len(matches) > 1 {
		return matches[1], nil
	}

	return "", nil
}

func (n *nftablesFilterer) FinishFiltering() error {
	cmd := exec.Command("nft", "--handle", "list", "chain", "inet", "filter", "output")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "No such file or directory") {
			return nil // Chain or table doesn't exist, so no rules to flush.
		}
		return fmt.Errorf("failed to list nftables rules with handles: %w, output: %s", err, string(output))
	}

	re := regexp.MustCompile(fmt.Sprintf(`comment \"%s\".* # handle (\\d+)`, n.identifier))
	matches := re.FindAllStringSubmatch(string(output), -1)

	for _, match := range matches {
		if len(match) > 1 {
			handle := match[1]
			cmd := exec.Command("nft", "delete", "rule", "inet", "filter", "output", "handle", handle)
			if err := cmd.Run(); err != nil {
				log.Printf("Failed to delete nftables rule with handle %s: %v", handle, err)
			}
		}
	}
	return nil
}
