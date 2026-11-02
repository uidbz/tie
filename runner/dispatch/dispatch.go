// Package dispatch decides WHERE a job runs in a fleet of runners: this
// runner or a capable backend, degrading from "run now" to
// "queue there" to "nobody can". It is also what the capabilities and
// fleet endpoints exist to answer.
//
// The rules (frozen behavior): prefer this runner when it is capable
// and immediately available; otherwise forward to the first capable
// backend that has room right now; when no runner is idle, round-robin
// among all capable runners and force-queue there so the job still gets
// accepted somewhere.
package dispatch

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
)

// Router answers the where. Its accessors are functions so hot-reloaded
// capabilities and backends take effect on the next decision.
type Router struct {
	// Capabilities lists the job types this runner serves.
	Capabilities func() []string
	// Backends lists the peer runner URLs.
	Backends func() []string
	// ResolveSpec maps a job type to its resource sizing. nil means
	// unknown types fit trivially (they are rejected or routed
	// elsewhere anyway); sizing is assumed consistent fleet-wide.
	ResolveSpec func(jobType string) (executor.Spec, bool)
	// CanFit reports whether the spec fits this runner's free capacity.
	CanFit func(spec executor.Spec) bool
	// Client probes backends and forwards requests; nil uses a 60s
	// timeout default. A flat timeout is safe: forwarded /run returns on
	// submission, /status polls return quickly.
	Client *http.Client

	rr atomic.Uint64
}

func (d *Router) client() *http.Client {
	if d.Client == nil {
		return &http.Client{Timeout: 60 * time.Second}
	}
	return d.Client
}

// Select decides where a job should run. It returns the backend URL to
// forward to (empty if running locally), whether to run locally, and
// whether the chosen runner must force-queue the job (because no runner
// was idle).
func (d *Router) Select(job api.JobRequest, selfURL string) (target string, runLocal bool, forceQueue bool) {
	var caps []string
	if d.Capabilities != nil {
		caps = d.Capabilities()
	}
	localCapable := slices.Contains(caps, job.JobType)
	var spec executor.Spec
	if d.ResolveSpec != nil {
		spec, _ = d.ResolveSpec(job.JobType) // zero spec fits trivially
	}

	canFit := false
	if d.CanFit != nil {
		canFit = d.CanFit(spec)
	}
	if localCapable && (job.ForceAddToQueue || canFit) {
		return "", true, job.ForceAddToQueue
	}

	suitable := []string{}
	if localCapable {
		suitable = append(suitable, selfURL)
	}
	for _, x := range d.backendList() {
		server := d.CapabilitiesOf(x)
		if !slices.Contains(server.Capabilities, job.JobType) {
			continue
		}
		// A backend can start the job now if the spec fits its free
		// resources.
		hasRoom := server.FreeCPU >= spec.CPU && server.FreeRAMMB >= spec.RAMMB && server.FreeGPU >= spec.GPU
		if server.TotalCPU == 0 {
			hasRoom = server.IsAvailable // older runner without capacity reporting
		}
		if hasRoom {
			return x, false, false // has room: forward, let it run now
		}
		suitable = append(suitable, x)
	}

	if len(suitable) == 0 {
		return "", false, false // no capable runner anywhere
	}

	// Nobody idle: round-robin among capable runners and queue there.
	idx := d.rr.Add(1)
	chosen := suitable[idx%uint64(len(suitable))]
	if chosen == selfURL {
		return "", true, true
	}
	return chosen, false, true
}

func (d *Router) backendList() []string {
	if d.Backends == nil {
		return nil
	}
	return d.Backends()
}

// CapabilitiesOf probes a backend's /api/capabilities. Unreachable or
// broken backends return the zero reply (never blocks the decision:
// a zero reply is simply not capable).
func (d *Router) CapabilitiesOf(backend string) api.CapabilityReply {
	url := backend + "/api/capabilities"
	resp, err := d.client().Get(url)
	if err != nil {
		slog.Error("Failed to get capabilities from backend", "backend", backend, "error", err)
		return api.CapabilityReply{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("Unexpected status from backend", "backend", backend, "status", resp.Status)
		return api.CapabilityReply{}
	}
	var reply api.CapabilityReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		slog.Error("Failed to decode capabilities response", "backend", backend, "error", err)
		return api.CapabilityReply{}
	}
	return reply
}

// Forward answers w with the result of replaying the request against
// backend+RequestURI (method, body, and headers carried through). It
// returns the response body so callers can react to it — the run path
// records the remote job as a redirect pointer, and later status polls
// arriving here get forwarded to the runner that owns the job.
func Forward(client *http.Client, backend string, w http.ResponseWriter, r *http.Request) []byte {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	proxyReq, err := http.NewRequest(r.Method, backend+r.RequestURI, r.Body)
	if err != nil {
		http.Error(w, "Backend error: "+err.Error(), http.StatusBadGateway)
		return nil
	}
	proxyReq.Header = r.Header

	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Backend error: "+err.Error(), http.StatusBadGateway)
		return nil
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	w.WriteHeader(resp.StatusCode)
	w.Write(bodyBytes)
	return bodyBytes
}
