package lib

import (
	"fmt"
	"math"
	"net"
	"time"
)

func SeqIncrement(seq uint32) uint32 {
	return uint32(uint64(seq) + 1) // implicit modulo operation included
}

func SeqIncrementBy(seq, inc uint32) uint32 {
	return uint32(uint64(seq) + uint64(inc)) // implicit modulo operation included
}

// SEQ compare function with SEQ wraparound in mind
func isGreater(seq1, seq2 uint32) bool {
	if seq1 == seq2 {
		return false
	}
	// Calculate direct difference
	var diff, wrapdiff, distance int64
	diff = int64(seq1) - int64(seq2)
	if diff < 0 {
		diff = -diff
	}
	wrapdiff = int64(math.MaxUint32 + 1 - diff)

	// Choose the shorter distance
	if diff < wrapdiff {
		distance = diff
	} else {
		distance = wrapdiff
	}

	// Check if the first sequence number is "greater"
	return (distance+int64(seq2))%(math.MaxUint32+1) == int64(seq1)
}

func isGreaterOrEqual(seq1, seq2 uint32) bool {
	return isGreater(seq1, seq2) || (seq1 == seq2)
}

func isLess(seq1, seq2 uint32) bool {
	return !isGreaterOrEqual(seq1, seq2)
}

func isLessOrEqual(seq1, seq2 uint32) bool {
	return !isGreater(seq1, seq2)
}

type TimeoutError struct {
	msg string
}

func (e *TimeoutError) Error() string {
	return e.msg
}

func (e *TimeoutError) Timeout() bool {
	return true
}

func (e *TimeoutError) Temporary() bool {
	return false
}

// sleep for n milliseconds
func SleepForMs(n int) {
	timeout := time.After(time.Duration(n) * time.Millisecond)
	<-timeout // Wait on the channel
}

// findLocalIP selects a local IP address that is in the same subnet as the targetIP,
// or falls back to an IP in the same subnet as the default gateway.
func findLocalIP(targetIP string) (string, error) {
	// Parse target IP
	target := net.ParseIP(targetIP)
	if target == nil {
		return "", fmt.Errorf("invalid target IP: %s", targetIP)
	}

	// Assume a /24 subnet mask for simplicity
	targetNet := &net.IPNet{
		IP:   target,
		Mask: net.CIDRMask(24, 32), // 255.255.255.0
	}

	// Get all network interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %v", err)
	}

	// Check for an interface in the same subnet as targetIP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue // Skip down or loopback interfaces
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() == nil {
				continue // Skip IPv6
			}

			// Check if local IP is in the same subnet as targetIP
			localNet := &net.IPNet{
				IP:   ip,
				Mask: net.CIDRMask(24, 32),
			}
			if localNet.Contains(target) || targetNet.Contains(ip) {
				return ip.String(), nil
			}
		}
	}

	// Fallback: Return the first non-loopback IPv4 address
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable local IP found for target %s", targetIP)
}
