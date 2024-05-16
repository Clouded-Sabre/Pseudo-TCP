package lib

import (
	"math"
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
