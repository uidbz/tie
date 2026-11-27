package service

import (
	"fmt"
	"net"
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

// portBindFree reports whether a TCP listener can actually bind the port
// on this host. The pool's own bookkeeping can drift: a recycled queue
// entry may name a port a live container still holds, and handing it out
// starts a service onto a conflict. The bind probe is the source of truth
// at both ends of the pool. Go listeners set SO_REUSEADDR, so TIME_WAIT
// on the runner's own sockets never produces a false "in use".
func portBindFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// Next returns the next unreserved port: a recycled one first, then the
// ascending frontier. Queue entries whose port is still bound on the host
// are dropped, not handed out — the entry is stale, and the real holder
// will re-queue the port through Release once it actually stops.
func (p *PortPool) Next() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.available) > 0 {
		port := p.available[0]
		p.available = p.available[1:]
		if p.reserved[port] {
			continue
		}
		if !portBindFree(port) {
			continue // stale queue entry: a live socket holds this port
		}
		return port, nil
	}

	for p.base <= p.Max {
		port := p.base
		p.base++
		if !p.reserved[port] && portBindFree(port) {
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

// Release returns a dynamic port to the pool. Ports outside the range,
// reserved (fixed services keep theirs across stop/start cycles), or
// still bound on the host are ignored: re-queueing a bound port poisons
// the queue, because the next Next() would hand a live port to a new
// service.
func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if port >= p.Min && port <= p.Max && !p.reserved[port] && portBindFree(port) {
		p.available = append(p.available, port)
	}
}
