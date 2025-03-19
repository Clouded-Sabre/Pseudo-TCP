//go:build linux
// +build linux

package lib

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

const (
	myComment = "PCP: "
) // Comment to identify the rules

// addAFilteringRule adds an iptables rule to drop RST packets originating from the given IP and port.
// It first checks if the rule already exists to avoid duplicates.
func addAFilteringRule(dstAddr string, dstPort int) error {
	// Construct the rule string to check for its existence
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -d %s --dport %d -m comment --comment \"%s\" -j DROP", dstAddr, dstPort, myComment)

	// List all rules in the OUTPUT chain
	cmd := exec.Command("iptables", "-S", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}

	// Check if the rule already exists
	if strings.Contains(string(output), ruleCheck) {
		// Rule already exists, no need to add it again
		fmt.Printf("Rule already exists: %s\n", ruleCheck)
		return nil
	}

	// Rule does not exist, add it
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", myComment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}

	fmt.Printf("Successfully added rule: %s\n", ruleCheck)
	return nil
}

// removeIptablesRule removes the iptables rule that was added for dropping RST packets.
func removeAFilteringRule(dstAddr string, dstPort int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", myComment, "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		// If there is an error executing the command, return the error
		return err
	}

	return nil
}

// finishFiltering removes all iptables rules with the "myAppRule" comment
func finishFiltering() error {
	// List all rules in the INPUT chain
	cmd := exec.Command("iptables", "-S", "INPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}

	// Identify and delete rules with the "myAppRule" comment
	var deleteErrors []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "--comment \""+myComment+"\"") {
			// Replace "-A" with "-D" to delete the rule
			deleteCmd := strings.Replace(line, "-A", "-D", 1)
			cmd := exec.Command("sh", "-c", "iptables "+deleteCmd)
			if out, err := cmd.CombinedOutput(); err != nil {
				deleteErrors = append(deleteErrors, fmt.Sprintf("%s\nError: %s", deleteCmd, string(out)))
			}
		}
	}

	// Report any deletion failures
	if len(deleteErrors) > 0 {
		return fmt.Errorf("some rules failed to delete:\n%s", strings.Join(deleteErrors, "\n"))
	}

	return nil
}

// addAFilteringRule adds an iptables rule to block RST packets originating from the given IP and port.
func addAServerFilteringRule(srcAddr string, srcPort int) error {
	// Construct the rule string to check for its existence
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -s %s --sport %d -j DROP", srcAddr, srcPort)

	// List all rules in the OUTPUT chain
	cmd := exec.Command("iptables", "-S", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}

	// Check if the rule already exists
	if strings.Contains(string(output), ruleCheck) {
		// Rule already exists, no need to add it again
		log.Printf("Rule already exists: %s\n", ruleCheck)
		return nil
	}

	// Rule does not exist, add it
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}

	log.Printf("Successfully added rule: %s\n", ruleCheck)
	return nil
}

// removeAFilteringRule removes the iptables rule that blocks RST packets for the given IP and port.
func removeAServerFilteringRule(srcAddr string, srcPort int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove iptables rule: %v", err)
	}

	log.Printf("Successfully removed iptables rule for %s:%d\n", srcAddr, srcPort)
	return nil
}
