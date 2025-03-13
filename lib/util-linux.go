//go:build linux
// +build linux

package lib

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	myComment = "PCP: "
) // Comment to identify the rules

// addIptablesRule adds an iptables rule to drop RST packets originating from the given IP and port.
func addAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	cmd := exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", myComment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// removeIptablesRule removes the iptables rule that was added for dropping RST packets.
func removeAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", myComment, "-j", "DROP")

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
