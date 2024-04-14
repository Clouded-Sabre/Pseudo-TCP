package lib

import (
	"fmt"
	"sync"
	"time"
)

// Chunk represents a single chunk of payload
type Chunk struct {
	Data           []byte
	Length         int
	LastAllocation time.Time
	CallStack      []string
}

// NewChunk creates a new chunk with the given length
func NewChunk(length int) *Chunk {
	return &Chunk{
		Data:   make([]byte, length),
		Length: 0,
		//LastAllocation: time.Time{},
		CallStack: nil,
	}
}

// AddCallStack adds a function string to the call stack of the chunk
func (c *Chunk) AddCallStack(funcString string) {
	c.CallStack = append(c.CallStack, funcString)
}

// PopCallStack removes the last function string from the call stack of the chunk
func (c *Chunk) PopCallStack() {
	if len(c.CallStack) > 0 {
		c.CallStack = c.CallStack[:len(c.CallStack)-1]
	}
}

// PrintCallStack prints the call stack of the chunk
func (c *Chunk) PrintCallStack() {
	fmt.Print("Call Stack:")
	for _, call := range c.CallStack {
		fmt.Printf(" -> %s", call)
	}
	fmt.Println()
}

// Reset resets necessary fields after chunk is returned
func (c *Chunk) Reset() {
	c.Length = 0
	c.LastAllocation = time.Time{}
	c.CallStack = nil
}

// IsTimedOut checks if an allocated chunk is been held for too long
func (c *Chunk) IsTimedOut(timeout time.Duration) bool {
	return time.Since(c.LastAllocation) > timeout
}

// GetAgeSeconds returns how long has it been since the chunk was last allocated
func (c *Chunk) GetAgeSeconds() float64 {
	return time.Since(c.LastAllocation).Seconds()
}

// Copy copies srcByteSlice to the chunk's byte slice
func (c *Chunk) Copy(srcByteSlice []byte) {
	c.Length = copy(c.Data, srcByteSlice)
}

// PayloadPool represents a pool of packet payloads
type PayloadPool struct {
	chunks    []*Chunk
	available int
	mtx       sync.Mutex
	closed    bool
}

// NewPayloadPool creates a new payload pool with the specified capacity
func NewPayloadPool(capacity int, chunkLength int) *PayloadPool {
	chunks := make([]*Chunk, capacity)
	for i := 0; i < capacity; i++ {
		chunks[i] = NewChunk(chunkLength)
	}
	return &PayloadPool{
		chunks:    chunks,
		available: capacity,
		closed:    false,
	}
}

// GetPayload retrieves a payload from the pool
func (p *PayloadPool) GetPayload() *Chunk {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.closed || p.available == 0 {
		return NewChunk(0) // Create a new chunk if pool is closed or empty
	}

	chunk := p.chunks[p.available-1]
	chunk.LastAllocation = time.Now() // Update LastAllocation when chunk is retrieved
	p.available--
	return chunk
}

// ReturnPayload returns a payload to the pool
func (p *PayloadPool) ReturnPayload(chunk *Chunk) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.closed || p.available == len(p.chunks) {
		return // Pool is closed or full, discard the chunk
	}

	chunk.Reset()
	p.chunks[p.available] = chunk
	p.available++
}

// Close closes the payload pool
func (p *PayloadPool) Close() {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if !p.closed {
		p.closed = true
	}
}

// AvailableChunks returns the number of available chunks in the pool
func (p *PayloadPool) AvailableChunks() int {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	return p.available
}

// Example usage
/*func main() {
	pool := NewPayloadPool(2000, 1500) // Adjust capacity and chunk length as needed

	// Example of getting and returning payloads
	chunk := pool.GetPayload()
	// Use the chunk
	// ...

	// After using the chunk, return it to the pool
	pool.ReturnPayload(chunk)

	// Check the number of available chunks
	availableChunks := pool.AvailableChunks()
	println("Available chunks:", availableChunks)

	// Close the pool when it's no longer needed
	pool.Close()
}*/
