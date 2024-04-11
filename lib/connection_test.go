package lib

import (
	"testing"
)

func TestIsGreater(t *testing.T) {
	// Test cases where the first number is greater than the second
	testCases := []struct {
		seq1     uint32
		seq2     uint32
		expected bool
	}{
		{seq1: 10, seq2: 5, expected: true},  // Direct comparison
		{seq1: 5, seq2: 10, expected: false}, // Direct comparison
		//{seq1: 4294967295, seq2: 5, expected: true},          // Wrap-around case
		{seq1: 5, seq2: 4294967295, expected: true},           // Inverse wrap-around case
		{seq1: 4294967295, seq2: 5, expected: false},          // Inverse wrap-around case
		{seq1: 2147483647, seq2: 2147483646, expected: true},  // Close to wrap-around boundary
		{seq1: 2147483646, seq2: 2147483647, expected: false}, // Close to wrap-around boundary
		{seq1: 0, seq2: 4294967295, expected: true},           // Full wrap-around
		{seq1: 4294967295, seq2: 0, expected: false},          // Full wrap-around
	}

	for _, tc := range testCases {
		result := isGreater(tc.seq1, tc.seq2)
		if result != tc.expected {
			t.Errorf("For (%d, %d), expected %t, but got %t", tc.seq1, tc.seq2, tc.expected, result)
		}
	}
}
