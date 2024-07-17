package lib

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// PortPool manages a pool of port numbers for TCP local port number allocation
// it makes use of ring pool implemetation
type PortPool struct {
	ports           []int
	capacity        int
	minPort         int
	maxPort         int
	readIdx         int
	writeIdx        int
	isFull, isEmpty bool
	allocatedMap    map[int]time.Time
	mtx             sync.Mutex
}

// NewPortPool creates a new port pool with the specified capacity and port range
func newPortPool(minPort, maxPort int) *PortPool {
	capacity := maxPort - minPort + 1

	// Generate a random permutation of indices
	perm := rand.Perm(capacity)

	ports := make([]int, capacity)
	for i, v := range perm {
		ports[i] = minPort + v // ports becomes a ramdon sequence of intergers from minPort to maxPort
	}

	p := &PortPool{
		ports:        ports,
		capacity:     capacity,
		minPort:      minPort,
		maxPort:      maxPort,
		allocatedMap: make(map[int]time.Time),
		isEmpty:      false,
		isFull:       false,
	}

	return p
}

// AllocatePort retrieves a random port from the pool
func (p *PortPool) allocatePort() (int, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	// Check if the pool is empty
	if p.isEmpty {
		log.Println("Port allocation: port pool is empty. Cannot allocate")
		return 0, fmt.Errorf("port pool is empty")
	}

	port := p.ports[p.readIdx]
	p.readIdx = (p.readIdx + 1) % p.capacity // Move read index circularly

	if p.readIdx == p.writeIdx {
		p.isEmpty = true
	}

	p.isFull = false

	// Add the port to allocatedMap with the current timestamp
	p.allocatedMap[port] = time.Now()

	return port, nil
}

// ReturnPort returns a port to the pool
func (p *PortPool) returnPort(port int) error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if port < p.minPort || port > p.maxPort {
		log.Println("Port Pool: returned a port out of range")
		return fmt.Errorf("port out of range")
	}

	if p.isFull {
		log.Println("Deallocation: Port pool is full, cannot return more ports")
		return fmt.Errorf("port pool is full")
	}

	// Reuse the port number
	p.ports[p.writeIdx] = port
	p.writeIdx = (p.writeIdx + 1) % p.capacity

	if p.writeIdx == p.readIdx {
		p.isFull = true
	}

	p.isEmpty = false

	// Remove the port from allocatedMap
	delete(p.allocatedMap, port)

	return nil
}

// AvailablePorts returns the number of available ports in the pool
/*func (p *PortPool) getNumAvailablePorts() int {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.readIdx > p.writeIdx {
		return p.capacity - (p.readIdx - p.writeIdx)
	} else if p.readIdx < p.writeIdx {
		return p.writeIdx - p.readIdx
	} else {
		if p.isEmpty {
			return 0
		}
		if p.isFull {
			return p.capacity
		}
		return 0
	}
}*/
