//go:build linux
// +build linux

package filter

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

/*const (
	myComment = "PCP: "
) // Comment to identify the rules */

type filterImpl struct {
	comment string
}

func NewFilter(identifier string) (Filter, error) {
	if isIptablesEnabled() != nil {
		return nil, fmt.Errorf("iptables is not enabled or available")
	}
	return &filterImpl{
		comment: identifier,
	}, nil
}

// isIptablesEnabled checks if iptables is enabled and available on the system.
func isIptablesEnabled() error {
	// Run the iptables command to list rules in the OUTPUT chain
	cmd := exec.Command("iptables", "-L", "OUTPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables is not enabled or available: %v\nOutput: %s", err, string(output))
	}

	// If the command succeeds, iptables is enabled
	log.Println("iptables is enabled and available.")
	return nil
}

// addAFilteringRule adds an iptables rule to drop RST packets originating from the given IP and port.
// It first checks if the rule already exists to avoid duplicates.
func (f *filterImpl) AddAClientFilteringRule(dstAddr string, dstPort int) error {
	// Construct the rule string to check for its existence
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -d %s --dport %d -m comment --comment \"%s\" -j DROP", dstAddr, dstPort, f.comment)

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
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}

	fmt.Printf("Successfully added rule: %s\n", ruleCheck)
	return nil
}

// RemoveIptablesRule removes the iptables rule that was added for dropping RST packets.
func (f *filterImpl) RemoveAClientFilteringRule(dstAddr string, dstPort int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-d", dstAddr, "--dport", strconv.Itoa(dstPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		// If there is an error executing the command, return the error
		return err
	}

	return nil
}

// finishFiltering removes all iptables rules with the "myAppRule" comment
func (f *filterImpl) FinishFiltering() error {
	// List all rules in the INPUT chain
	cmd := exec.Command("iptables", "-S", "INPUT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list iptables rules: %v\nOutput: %s", err, string(output))
	}

	// Identify and delete rules with the "myAppRule" comment
	var deleteErrors []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "--comment \""+f.comment+"\"") {
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
func (f *filterImpl) AddAServerFilteringRule(srcAddr string, srcPort int) error {
	// Construct the rule string to check for its existence
	ruleCheck := fmt.Sprintf("-A OUTPUT -p tcp --tcp-flags RST RST -s %s --sport %d -m comment --comment %s -j DROP", srcAddr, srcPort, f.comment)

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
	cmd = exec.Command("iptables", "-A", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add iptables rule: %v", err)
	}

	log.Printf("Successfully added rule: %s\n", ruleCheck)
	return nil
}

// removeAFilteringRule removes the iptables rule that blocks RST packets for the given IP and port.
func (f *filterImpl) RemoveAServerFilteringRule(srcAddr string, srcPort int) error {
	// Construct the command to delete the iptables rule
	cmd := exec.Command("iptables", "-D", "OUTPUT", "-p", "tcp", "--tcp-flags", "RST", "RST", "-s", srcAddr, "--sport", strconv.Itoa(srcPort), "-m", "comment", "--comment", f.comment, "-j", "DROP")

	// Execute the command to delete the iptables rule
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove iptables rule: %v", err)
	}

	log.Printf("Successfully removed iptables rule for %s:%d\n", srcAddr, srcPort)
	return nil
}

func (f *filterImpl) AddIcmpSrcFilteringRule(srcAddr string) error {
	// adds an iptables rule to drop icmp port unreachable packets originating from the given IP and port.
	// Build the iptables command
	cmd := exec.Command("iptables", "-A", "OUTPUT",
		"-s", srcAddr,
		"-p", "icmp",
		"--icmp-type", "3/3",
		//"-m", "u32",
		//"--u32", fmt.Sprintf("0 >> 22 & 0x3C @ 16 >> 16 = %s", portStr),
		"-j", "REJECT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply iptables rule: %v, command: %s", err, strings.Join(cmd.Args, " "))
	}

	log.Printf("Successfully added rule: %s\n", cmd.String())
	return nil
}

func (f *filterImpl) RemoveIcmpSrcFilteringRule(srcAddr string) error {
	// removes the iptables rule that blocks icmp port unreachable packets for the given IP and port.
	// Build the iptables command
	cmd := exec.Command("iptables", "-D", "OUTPUT",
		"-s", srcAddr,
		"-p", "icmp",
		"--icmp-type", "3/3",
		//"-m", "u32",
		//"--u32", fmt.Sprintf("0 >> 22 & 0x3C @ 16 >> 16 = %s", portStr),
		"-j", "REJECT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove iptables rule: %v, command: %s", err, strings.Join(cmd.Args, " "))
	}

	log.Printf("Successfully removed rule: %s\n", cmd.String())
	return nil
}

func (f *filterImpl) AddIcmpDstFilteringRule(dstAddr string) error {
	// adds an iptables rule to drop icmp port unreachable packets destined to the given IP.
	// Build the iptables command
	cmd := exec.Command("iptables", "-A", "OUTPUT",
		"-d", dstAddr,
		"-p", "icmp",
		"--icmp-type", "3/3",
		//"-m", "u32",
		//"--u32", fmt.Sprintf("0 >> 22 & 0x3C @ 16 >> 16 = %s", portStr),
		"-j", "REJECT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to apply iptables rule: %v, command: %s", err, strings.Join(cmd.Args, " "))
	}

	log.Printf("Successfully added rule: %s\n", cmd.String())
	return nil
}

func (f *filterImpl) RemoveIcmpDstFilteringRule(dstAddr string) error {
	// removes the iptables rule that blocks icmp port unreachable packets to the given IP.
	// Build the iptables command
	cmd := exec.Command("iptables", "-D", "OUTPUT",
		"-d", dstAddr,
		"-p", "icmp",
		"--icmp-type", "3/3",
		//"-m", "u32",
		//"--u32", fmt.Sprintf("0 >> 22 & 0x3C @ 16 >> 16 = %s", portStr),
		"-j", "REJECT")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to remove iptables rule: %v, command: %s", err, strings.Join(cmd.Args, " "))
	}

	log.Printf("Successfully removed rule: %s\n", cmd.String())
	return nil
}
