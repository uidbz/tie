// Service activity tracking: the state behind the image entry flags
// ServiceInactivityTimeout and ServiceStartOnAccess. The reverse proxy
// stamps every service request and every WebSocket tunnel open/close
// into a per-service last-access clock (via the hooks in
// runner/proxy.Config); a background sweep stops services whose idle
// window has expired, using the same StopTask path as the services
// HTTP API. Raw-TCP services have no proxy path, so their clock only
// moves at start/adoption — they can still be reaped once the timeout
// elapses with nothing touching them.
//
// Start-on-access is the inverse: a parked service whose entry sets
// ServiceStartOnAccess is started through the normal StartService path
// when the proxy hits it, and the waking request waits (bounded) for
// the backend to accept connections before proxying.
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Touch records "this service was just used", restarting its idle
// countdown. Wired into the proxy as Config.Activity.
func (m *Manager) Touch(service string) {
	m.actMu.Lock()
	m.lastAccess[service] = time.Now()
	m.actMu.Unlock()
}

// clearActivity drops all activity state for a service (stopped,
// unregistered). The next start re-initialises the clock.
func (m *Manager) clearActivity(service string) {
	m.actMu.Lock()
	delete(m.lastAccess, service)
	delete(m.tunnels, service)
	m.actMu.Unlock()
}

// TunnelOpen marks a live WebSocket tunnel. While the count is >0 the
// inactivity sweep leaves the service alone; the open event itself
// stamps the activity clock.
func (m *Manager) TunnelOpen(service string) {
	m.actMu.Lock()
	m.tunnels[service]++
	m.lastAccess[service] = time.Now()
	m.actMu.Unlock()
}

// TunnelClosed releases one tunnel slot and re-stamps activity: the
// idle countdown starts fresh at disconnect, so a long-lived tunnel
// cannot leave behind a back-dated clock that gets reaped on the next
// sweep tick.
func (m *Manager) TunnelClosed(service string) {
	m.actMu.Lock()
	if m.tunnels[service] > 0 {
		m.tunnels[service]--
	}
	if m.tunnels[service] == 0 {
		delete(m.tunnels, service)
	}
	m.lastAccess[service] = time.Now()
	m.actMu.Unlock()
}

// lastAccessFor returns the recorded last-activity time, or now when
// there is no record. An unrecorded running service (e.g. one whose
// timeout was only just configured, or state that drifted) counts as
// just-used, so a sweep can never reap it on sight.
func (m *Manager) lastAccessFor(service string, now time.Time) time.Time {
	m.actMu.Lock()
	defer m.actMu.Unlock()
	if t, ok := m.lastAccess[service]; ok {
		return t
	}
	return now
}

func (m *Manager) tunnelsOpen(service string) int {
	m.actMu.Lock()
	defer m.actMu.Unlock()
	return m.tunnels[service]
}

// shouldStopForInactivity is the whole timeout decision as a pure
// function: disabled for timeout <= 0, open tunnels count as activity,
// and the idle window must be STRICTLY past the timeout so a service
// exactly on the boundary gets one more sweep.
func shouldStopForInactivity(now, lastAccess time.Time, timeoutSeconds, tunnels int) bool {
	if timeoutSeconds <= 0 {
		return false
	}
	if tunnels > 0 {
		return false
	}
	return now.Sub(lastAccess) > time.Duration(timeoutSeconds)*time.Second
}

// sweepInactivity stops every running service past its idle window.
// The watcher goroutine calls it with the wall clock; tests call it
// directly with a synthetic "now".
func (m *Manager) sweepInactivity(now time.Time) {
	m.mu.Lock()
	running := make([]string, 0, len(m.running))
	for name := range m.running {
		running = append(running, name)
	}
	m.mu.Unlock()

	for _, name := range running {
		img, ok := m.cfg.Images.Get(name)
		if !ok || img.ServiceInactivityTimeout <= 0 {
			continue
		}
		last := m.lastAccessFor(name, now)
		if !shouldStopForInactivity(now, last, img.ServiceInactivityTimeout, m.tunnelsOpen(name)) {
			continue
		}
		slog.Info("Stopping idle service", "service", name,
			"idle_seconds", int(now.Sub(last).Seconds()), "timeout_seconds", img.ServiceInactivityTimeout)
		if _, err := m.StopTask(name); err != nil {
			slog.Warn("Idle stop failed", "service", name, "error", err)
		}
	}
}

// watchInactivity sweeps on the configured cadence until the manager
// shuts down (StopAll closes the channel).
func (m *Manager) watchInactivity() {
	ticker := time.NewTicker(m.cfg.InactivityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.sweepInactivity(time.Now())
		}
	}
}

// Tunables (vars so tests can shrink the readiness wait).
var (
	startOnAccessReadyTimeout  = 90 * time.Second
	startOnAccessReadyInterval = 500 * time.Millisecond
)

// serviceStartCall is one in-flight start-on-access attempt. Concurrent
// requests to the same parked service attach to it instead of queueing
// duplicate docker runs.
type serviceStartCall struct {
	wg  sync.WaitGroup
	err error
}

// runServiceStartOnce runs start at most once per service at a time:
// the first caller executes it, concurrent callers wait for the same
// result. After completion the guard clears, so a later request can
// retry.
func (m *Manager) runServiceStartOnce(service string, start func() error) error {
	m.startMu.Lock()
	if call, inflight := m.startCalls[service]; inflight {
		m.startMu.Unlock()
		slog.Debug("Joining in-flight start-on-access", "service", service)
		call.wg.Wait()
		return call.err
	}
	call := &serviceStartCall{}
	call.wg.Add(1)
	m.startCalls[service] = call
	m.startMu.Unlock()

	call.err = start()
	call.wg.Done()

	m.startMu.Lock()
	if m.startCalls[service] == call {
		delete(m.startCalls, service)
	}
	m.startMu.Unlock()
	return call.err
}

// WakeOnAccess starts a parked service through the normal StartService
// path if its image entry opts into ServiceStartOnAccess — the same
// path the services API uses, so it gets identical port allocation,
// corpse sweep and readiness watching — then waits a bounded time for
// the backend to accept connections, so the waking request never
// proxies into a half-booted backend. It reports the published port.
//
// Only proxied webservices can wake this way: raw-TCP clients dial the
// pinned host port directly and never pass through the proxy. The
// flag being off keeps the historic behaviour exactly (the proxy
// answers "Service not running").
func (m *Manager) WakeOnAccess(service string) (int, bool) {
	img, found := m.cfg.Images.Get(service)
	if !found || !img.IsService || !img.ServiceStartOnAccess {
		return 0, false
	}
	if port, running := m.RunningPort(service); running {
		return port, true
	}
	err := m.runServiceStartOnce(service, func() error {
		slog.Info("Start-on-access: starting parked service", "service", service)
		if _, err := m.StartService(service); err != nil {
			return err
		}
		return m.waitBackendAccepting(service)
	})
	if err != nil {
		slog.Warn("Start-on-access failed", "service", service, "error", err)
		return 0, false
	}
	return m.RunningPort(service)
}

// waitBackendAccepting polls until the service's published port
// accepts a TCP connection. If the readiness watcher decides the
// container exited, the task leaves the running set and the wait fails
// fast instead of burning the whole window.
func (m *Manager) waitBackendAccepting(service string) error {
	deadline := time.Now().Add(startOnAccessReadyTimeout)
	for {
		port, running := m.RunningPort(service)
		if !running {
			return errors.New("service stopped again before it became reachable (it may have crashed right after start)")
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service did not accept connections on port %d within %s", port, startOnAccessReadyTimeout)
		}
		time.Sleep(startOnAccessReadyInterval)
	}
}
