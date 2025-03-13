//go:build darwin
// +build darwin

package lib

import (
	"fmt"
	"os/exec"
	"strings"
)

// Global anchor name for the application.
const anchor = "PCP_anchor"

// addAFilteringRule adds a new filtering rule to the anchor while leaving existing rules intact.
func addAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	// 1. Check if PF is enabled.
	enabled, err := isPFEnabled()
	if err != nil || !enabled {
		return fmt.Errorf("PF service is not enabled: %v", err)
	}

	// 2. Ensure the anchor exists.
	if err := pfManageAnchor(anchor, true); err != nil {
		return fmt.Errorf("failed to initialize anchor: %v", err)
	}

	// 3. Retrieve current rules from the anchor.
	currentRules, err := getPfRules(anchor)
	if err != nil {
		return fmt.Errorf("failed to retrieve current rules: %v", err)
	}

	// 4. Construct the new rule.
	newRule := fmt.Sprintf("block drop out inet proto tcp from %s port = %d to %s port = %d flags R/R",
		srcAddr, srcPort, dstAddr, dstPort)
	fmt.Println("Constructed rule:", newRule)

	// 5. Append the new rule if it does not already exist.
	if !containsRule(currentRules, newRule) {
		currentRules = append(currentRules, newRule)
	}

	// 6. Reload the anchor with the updated rule set.
	rulesText := strings.Join(currentRules, "\n") + "\n"
	if err := pfLoadRules(anchor, rulesText); err != nil {
		return fmt.Errorf("failed to load updated rules: %v", err)
	}

	// 7. Verify that the rule was added.
	if err := verifyRuleExactMatch(anchor, newRule); err != nil {
		return fmt.Errorf("rule verification failed: %v", err)
	}

	fmt.Printf("Successfully added rule:\n%s\n", newRule)
	return nil
}

// removeAFilteringRule removes a single filtering rule from the anchor while leaving the other rules intact.
func removeAFilteringRule(srcAddr, dstAddr string, srcPort, dstPort int) error {
	// 1. Retrieve current rules.
	currentRules, err := getPfRules(anchor)
	if err != nil {
		return fmt.Errorf("failed to retrieve current rules: %v", err)
	}

	// 2. Construct the rule to remove.
	ruleToRemove := fmt.Sprintf("block drop out inet proto tcp from %s port = %d to %s port = %d flags R/R",
		srcAddr, srcPort, dstAddr, dstPort)
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
	if err := pfLoadRules(anchor, rulesText); err != nil {
		return fmt.Errorf("failed to load updated rules: %v", err)
	}

	fmt.Println("Successfully removed rule.")
	return nil
}

// removeAnchor removes the anchor along with all its rules.
func finishFiltering() error {
	return pfManageAnchor(anchor, false)
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

// pfManageAnchor creates or removes an anchor.
// If create is true, it creates (or ensures) the anchor exists;
// if false, it disables/removes the anchor.
func pfManageAnchor(anchor string, create bool) error {
	action := "anchor"
	if !create {
		action = "no anchor"
	}
	cmd := exec.Command("pfctl", "-a", ".", "-f", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s \"%s\"\n", action, anchor))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pf anchor operation failed: %v\nCommand output: %s", err, string(output))
	}
	return nil
}

// getPfRules retrieves the current PF rules for the given anchor.
func getPfRules(anchor string) ([]string, error) {
	cmd := exec.Command("pfctl", "-a", anchor, "-s", "rules")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to query PF rules: %v\nOutput: %s", err, string(output))
	}

	lines := strings.Split(string(output), "\n")
	var rules []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			rules = append(rules, trimmed)
		}
	}
	return rules, nil
}

// pfLoadRules loads the given rules (as a string) into the specified anchor.
func pfLoadRules(anchor, rules string) error {
	cmd := exec.Command("pfctl", "-a", anchor, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to load PF rules: %v\nCommand output: %s", err, string(output))
	}
	return nil
}

// verifyRuleExactMatch checks if the expected rule exactly appears in the anchor.
func verifyRuleExactMatch(anchor, expectedRule string) error {
	cmd := exec.Command("pfctl", "-a", anchor, "-s", "rules")
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
