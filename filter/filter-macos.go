//go:build darwin
// +build darwin

package filter

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// filterImpl is the implementation of the Filter interface for macOS.
type filterImpl struct {
	anchor          string
	udpServerFilter *udpServerFilter // Struct containing the shared method
}

func NewFilter(identifier string) (Filter, error) {
	// 1. Check if PF is enabled.
	enabled, err := isPFEnabled()
	if err != nil || !enabled {
		return nil, fmt.Errorf("PF service is not enabled: %v", err)
	}

	// 2. Check if libpcap is installed.
	if err := isLibpcapInstalled(); err != nil {
		return nil, fmt.Errorf("libpcap check failed: %v", err)
	}

	// 3. Ensure the anchor reference exists in /etc/pf.conf.
	if refExists, err := pfCheckAnchor(identifier); err != nil {
		return nil, fmt.Errorf("failed to check anchor reference in /etc/pf.conf: %v", err)
	} else {
		if !refExists {
			return nil, fmt.Errorf("anchor reference to %s does not exists in /etc/pf.conf. Please add it", identifier)
		}
	}

	return &filterImpl{
		anchor:          identifier,
		udpServerFilter: NewUdpServerFilter(),
	}, nil
}

// addAFilteringRule adds a new filtering rule to the anchor while leaving existing rules intact.
func (f *filterImpl) AddTcpClientFiltering(dstAddr string, dstPort int) error {

	// 3. Retrieve current rules from the anchor.
	currentRules, err := getPfRules(f.anchor)
	if err != nil {
		return fmt.Errorf("failed to retrieve current rules: %v", err)
	}

	// 4. Construct the new rule.
	//newRule := fmt.Sprintf("block drop out inet proto tcp to %s port = %d flags R/R", dstAddr, dstPort)
	newRule := fmt.Sprintf("block drop out quick inet proto tcp from any to %s port = %d flags R/R", dstAddr, dstPort)

	// 5. Append the new rule if it does not already exist.
	if !containsRule(currentRules, newRule) {
		currentRules = append(currentRules, newRule)
	}

	// 6. Reload the anchor with the updated rule set.
	//rulesText := strings.Join(currentRules, "\n") + "\n"
	rulesText := strings.Join(currentRules, "\n")
	if err := pfLoadRules(f.anchor, rulesText); err != nil {
		return fmt.Errorf("failed to load updated rules: %v", err)
	}

	// 7. Verify that the rule was added.
	if err := verifyRuleExactMatch(f.anchor, newRule); err != nil {
		return fmt.Errorf("rule verification failed: %v", err)
	}

	fmt.Printf("Successfully added rule:\n%s\n", newRule)
	return nil
}

// removeAFilteringRule removes a single filtering rule from the anchor while leaving the other rules intact.
func (f *filterImpl) RemoveTcpClientFiltering(dstAddr string, dstPort int) error {
	// 1. Retrieve current rules.
	currentRules, err := getPfRules(f.anchor)
	if err != nil {
		return fmt.Errorf("failed to retrieve current rules: %v", err)
	}

	// 2. Construct the rule to remove.
	ruleToRemove := fmt.Sprintf("block drop out quick inet proto tcp from any to %s port = %d flags R/R", dstAddr, dstPort)
	fmt.Println("Removing rule:", ruleToRemove)

	// 3. Filter out the rule from the current rules.
	updatedRules := []string{}
	for _, rule := range currentRules {
		if strings.TrimSpace(rule) != strings.TrimSpace(ruleToRemove) {
			updatedRules = append(updatedRules, rule)
		}
	}

	// 4. Reload the anchor with the updated rules.
	rulesText := strings.Join(updatedRules, "\n") + "\n"
	if err := pfLoadRules(f.anchor, rulesText); err != nil {
		return fmt.Errorf("failed to load updated rules: %v", err)
	}

	fmt.Println("Successfully removed rule.")
	return nil
}

// finishFiltering flushes all rules in the anchor and then removes the anchor itself.
func (f *filterImpl) FinishFiltering() error {
	// Flush the rules associated with the anchor
	cmdFlush := exec.Command("pfctl", "-a", f.anchor, "-F", "rules")
	output, err := cmdFlush.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to flush rules for anchor %s: %v\nCommand output: %s", f.anchor, err, string(output))
	}

	return nil
}

// ======== PF Control Functions ========

// isPFEnabled checks whether PF is enabled.
func isPFEnabled() (bool, error) {
	output, err := exec.Command("pfctl", "-s", "info").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("pfctl check failed: %v\nOutput: %s", err, string(output))
	}
	return strings.Contains(string(output), "Status: Enabled"), nil
}

// pfCheckAnchor checks if /etc/pf.conf contains a reference to the specified anchor.
// It returns true if the anchor is found, false otherwise.
func pfCheckAnchor(anchor string) (bool, error) {
	// Read the content of /etc/pf.conf
	data, err := os.ReadFile("/etc/pf.conf")
	if err != nil {
		return false, fmt.Errorf("failed to read /etc/pf.conf: %v", err)
	}

	// Construct the anchor reference string to look for.
	// This assumes the reference is written as: anchor "anchor_name"
	anchorRef := fmt.Sprintf("anchor \"%s\"", anchor)

	// Check if the content contains the anchor reference.
	if strings.Contains(string(data), anchorRef) {
		return true, nil
	}

	return false, nil
}

// getPfRules retrieves the current PF rules for the given anchor, keeping only "block" rules which is the only type of rules we use.
func getPfRules(anchor string) ([]string, error) {
	cmd := exec.Command("pfctl", "-a", anchor, "-s", "rules")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to query PF rules: %v\nOutput: %s", err, string(output))
	}

	lines := strings.Split(string(output), "\n")
	var rules []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "block") {
			rules = append(rules, trimmed)
		}
	}
	return rules, nil
}

func pfLoadRules(anchor, rules string) error {
	//fmt.Printf("Debug - PF Rules: %q\n", rules) // output quoted string which shows hidden characters

	cmd := exec.Command("sh", "-c", fmt.Sprintf("echo %q | sudo /sbin/pfctl -a %s -f -", rules, anchor))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to load PF rules: %v\nCommand output: %s", err, string(output))
	}
	return nil
}

// verifyRuleExactMatch checks if the expected rule exactly appears in the anchor.
func verifyRuleExactMatch(anchor, expectedRule string) error {
	cmd := exec.Command("/sbin/pfctl", "-a", anchor, "-s", "rules")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to query PF rules: %v", err)
	}

	expected := strings.TrimSpace(expectedRule)
	current := strings.TrimSpace(string(output))
	if !strings.Contains(current, expected) {
		return fmt.Errorf("rule does not match\nCurrent rules:\n%s\nExpected:\n%s", current, expected)
	}
	return nil
}

// containsRule checks if the given slice of rules contains the target rule.
func containsRule(rules []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, rule := range rules {
		if strings.TrimSpace(rule) == target {
			return true
		}
	}
	return false
}

func (f *filterImpl) AddTcpServerFiltering(srcAddr string, srcPort int) error {
	// we won't use macos as raw socket server, so we do nothing here

	return nil
}

func (f *filterImpl) RemoveTcpServerFiltering(srcAddr string, srcPort int) error {
	// we won't use macos as raw socket server, so we do nothing here

	return nil
}

func (f *filterImpl) AddUdpServerFiltering(srcAddr string) error { // srcAddr is the source ip address and port of the UDP server in "ip:port" format
	return f.udpServerFilter.AddUdpServerFiltering(srcAddr)
}

func (f *filterImpl) RemoveUdpServerFiltering(srcAddr string) error { // srcAddr is the source ip address and port of the UDP server in "ip:port" format
	return f.udpServerFilter.RemoveUdpServerFiltering(srcAddr)
}

// AddIcmpDstFilteringRule adds a filtering rule which block icmp unreacheable packets to dstAddr.
func (f *filterImpl) AddUdpClientFiltering(dstAddr string) error {
	// we won't use macos as udp raw socket client, so we do nothing here
	return nil
}

// RemoveIcmpDstFilteringRule removes a filtering rule which blocks icmp unreacheable packets to dstAddr.
func (f *filterImpl) RemoveUdpClientFiltering(dstAddr string) error {
	// we won't use macos as udp raw socket client, so we do nothing here
	return nil
}

// isLibpcapInstalled checks if libpcap is installed on the system.
func isLibpcapInstalled() error {
	// Check if tcpdump (which depends on libpcap) is available
	cmd := exec.Command("which", "tcpdump")
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return fmt.Errorf("libpcap is not installed or tcpdump is not available: %v", err)
	}

	// If tcpdump is found, libpcap is likely installed
	fmt.Println("libpcap is installed and available.")
	return nil
}
