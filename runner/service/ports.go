package service

import (
	"fmt"
	"sync"
)

// PortPool hands out host ports for published service containers.
// Dynamic webservices draw a fresh port per start; raw-TCP services own
// one reserved port forever (across restarts), so the allocator must
// never hand their port to anyone else.
type PortPool struct {
	Min, Max int

	mu        sync.Mutex
	base      int
	available []int
	reserved  map[int]bool
}

// NewPortPool covers [min, max]; reserved pre-marks fixed service ports.
func NewPortPool(min, max int, reserved ...int) *PortPool {
	p := &PortPool{Min: min, Max: max, base: min, reserved: map[int]bool{}}
	for _, port := range reserved {
		p.reserved[port] = true
	}
	return p
}

// Next returns the next unreserved port: a recycled one first, then the
// ascending frontier.
func (p *PortPool) Next() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.available) > 0 {
		port := p.available[0]
		p.available = p.available[1:]
		if !p.reserved[port] {
			return port, nil
		}
	}

	for p.base <= p.Max {
		port := p.base
		p.base++
		if !p.reserved[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", p.Min, p.Max)
}

// Reserve permanently marks a port as owned by a fixed service; the
// allocator skips it forever.
func (p *PortPool) Reserve(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reserved[port] = true
}

// Release returns a dynamic port to the pool. Ports outside the range or
// reserved (fixed services keep theirs across stop/start cycles) are
// ignored.
func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if port >= p.Min && port <= p.Max && !p.reserved[port] {
		p.available = append(p.available, port)
	}
}
