package lib

import "math"

func SeqIncrement(seq uint32) uint32 {
	return uint32(uint64(seq) + 1) // implicit modulo operation included
}

func SeqIncrementBy(seq, inc uint32) uint32 {
	return uint32(uint64(seq) + uint64(inc)) // implicit modulo operation included
}

// SEQ compare function with SEQ wraparound in mind
func isGreater(seq1, seq2 uint32) bool {
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
	return !isGreater(seq1, seq2)
}

func isLessOrEqual(seq1, seq2 uint32) bool {
	return isLess(seq1, seq2) || (seq1 == seq2)
}
