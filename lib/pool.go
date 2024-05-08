package lib

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Chunk represents a single chunk of payload
type Chunk struct {
	Data   []byte
	Length int // length of the real data
	// below is for debug purpose
	index          int
	LastAllocation time.Time
	CallStack      []string
	StayAtChannel  string
}

// NewChunk creates a new chunk with the given length
func NewChunk(index, length int) *Chunk {
	return &Chunk{
		Data:   make([]byte, length),
		Length: 0,
		index:  index,
		//LastAllocation: time.Time{},
		CallStack:     nil,
		StayAtChannel: "",
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

// AddToChannel assign a channel string to StayAtChannel of the chunk
func (c *Chunk) AddToChannel(channelString string) {
	c.StayAtChannel = channelString
}

func (c *Chunk) RemoveFromChannel() {
	c.StayAtChannel = ""
}

// PrintCallStack prints the call stack of the chunk
func (c *Chunk) PrintCallStack() {
	fmt.Print("Call Stack:")
	for _, call := range c.CallStack {
		fmt.Printf(" -> %s", call)
	}
	fmt.Println()
	if len(c.StayAtChannel) > 0 {
		fmt.Println("Chunk@channel:", c.StayAtChannel)
	}
	fmt.Println()
	fmt.Println("Payload:", string(c.Data[:c.Length]))
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
	chunks          []*Chunk
	capacity        int // total number of chunks in the pool
	chunkLength     int // length of each chunk (buffer length)
	readIdx         int
	writeIdx        int
	isFull, isEmpty bool
	allocatedMap    map[int]*Chunk
	mtx             sync.Mutex
}

// NewPayloadPool creates a new payload pool with the specified capacity
func NewPayloadPool(capacity int, chunkLength int) *PayloadPool {
	chunks := make([]*Chunk, capacity)
	for i := 0; i < capacity; i++ {
		chunks[i] = NewChunk(i, chunkLength)
	}

	p := &PayloadPool{
		chunks:       chunks,
		capacity:     capacity,
		chunkLength:  chunkLength,
		allocatedMap: make(map[int]*Chunk),
	}

	// start timeout checks for allocated chunks
	go p.CheckTimedOutChunks()

	return p
}

// GetPayload retrieves a payload from the pool
func (p *PayloadPool) GetPayload() *Chunk {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	// Check if the pool is empty
	if p.isEmpty {
		log.Println("Chunk allocation: payload pool is empty, allocate more chunk will impact performance till some chunks are returned")
		return NewChunk(p.capacity+1, p.chunkLength) // Pool is empty. Create new chunk manually
	}

	chunk := p.chunks[p.readIdx]
	chunk.LastAllocation = time.Now()
	p.readIdx = (p.readIdx + 1) % p.capacity // Move read index circularly

	if p.readIdx == p.writeIdx {
		p.isEmpty = true
	}

	p.isFull = false

	// Add the frame to allocatedMap
	p.allocatedMap[chunk.index] = chunk

	return chunk
}

// ReturnPayload returns a payload to the pool
func (p *PayloadPool) ReturnPayload(chunk *Chunk) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if chunk.index > p.capacity {
		// manually created chunk, just ignore it
		log.Println("Payload Pool: returned a manually created chunk")
		return
	}

	if p.isFull {
		log.Println("Deallocation: Pool is full, cannot return more chunk")
		return // Pool is full, discard the chunk
	}

	chunk.Reset()
	p.chunks[p.writeIdx] = chunk // Reuse the frame object
	// Check if the pool is full
	p.writeIdx = (p.writeIdx + 1) % p.capacity
	if p.writeIdx == p.readIdx {
		p.isFull = true
	}
	p.isEmpty = false

	// remove it from allocatedMap
	delete(p.allocatedMap, chunk.index)
	chunk = nil // Set the pointer to nil
}

// return the numnber of available frames
func (p *PayloadPool) AvailableChunks() int {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.readIdx > p.writeIdx {
		return p.capacity - (p.readIdx - p.writeIdx)
	} else if p.readIdx < p.writeIdx {
		return p.writeIdx - p.readIdx
	} else { // ==
		if p.isEmpty {
			return 0
		}
		if p.isFull {
			return p.capacity
		}
		return 0 // put it here just to please compiler hahaha
	}
}

// CheckTimedOutChunks checks every 5 seconds for chunks allocated more than 10 seconds ago
func (p *PayloadPool) CheckTimedOutChunks() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var count int
	for {
		<-ticker.C
		count = 0

		p.mtx.Lock()
		for _, chunk := range p.allocatedMap {
			if time.Since(chunk.LastAllocation) > 10*time.Second {
				chunk.PrintCallStack()
				count++
			}
		}
		log.Printf("Number of chunks allocated more than 10 seconds ago: %d\n", count)
		p.mtx.Unlock()
	}
}

var Pool *PayloadPool

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
