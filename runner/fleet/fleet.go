// Package fleet is the runner-side half of a runner fleet: the seed's
// host registry (new runners join by token, an admin approves, the seed
// hands back a generated config), the join client, forwarding for
// non-seed hosts, and deployed-image entry replication so any runner can
// authorize a job it ends up executing.
//
// Which config file to write and what a runner's config looks like is
// the deployment's: the join client returns the seed's config as raw
// JSON, and replication pushes an opaque entry. The registry file itself
// is owned here: a JSON table stored beside the config, mode 0600 (its
// entries carry per-host admin tokens).
package fleet

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Registry is the seed's host table. Hosts live in one JSON document
// ({"hosts": [...]}); Save writes it atomically.
type Registry struct {
	mu    sync.Mutex
	Hosts []*api.FleetHost `json:"hosts"`
	path  string
}

// Load reads the registry file; a missing or corrupt file yields a fresh
// empty registry (the seed re-accumulates hosts as they re-register).
func Load(path string) *Registry {
	r := &Registry{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, r); err != nil {
			r.Hosts = nil
		}
	}
	return r
}

// Save writes the registry atomically via temp file + rename, mode 0600
// (entries carry per-host admin tokens).
func (r *Registry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// newHostID/newAdminToken sizes match existing deployments: 10-hex host
// ids, 24-hex admin tokens.
func newHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("fleet: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func newHostID() string     { return newHex(5) }
func newAdminToken() string { return newHex(12) }

// AddPending registers a host, idempotently: re-registering the same URL
// returns the existing entry (this is how a joiner polls after approval;
// a removed host's URL may register fresh).
func (r *Registry) AddPending(name, url string, caps []string) *api.FleetHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.Hosts {
		if h.URL == url && h.Status != api.HostRemoved {
			return h
		}
	}
	h := &api.FleetHost{ID: newHostID(), Name: name, URL: url,
		Capabilities: caps, Status: api.HostPending,
		Added: time.Now().UTC().Format(time.RFC3339)}
	r.Hosts = append(r.Hosts, h)
	return h
}

// Approve flips pending -> active and mints the host's admin token.
// Unknown or non-pending ids return nil.
func (r *Registry) Approve(id string) *api.FleetHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.Hosts {
		if h.ID == id && h.Status == api.HostPending {
			h.Status = api.HostActive
			h.AdminToken = newAdminToken()
			h.Approved = time.Now().UTC().Format(time.RFC3339)
			return h
		}
	}
	return nil
}

// Remove tombstones a host (so re-registration stays an explicit act).
func (r *Registry) Remove(id string) *api.FleetHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.Hosts {
		if h.ID == id && h.Status != api.HostRemoved {
			h.Status = api.HostRemoved
			return h
		}
	}
	return nil
}

// List returns non-removed hosts, optionally active-only, sorted by name.
func (r *Registry) List(activeOnly bool) []*api.FleetHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*api.FleetHost
	for _, h := range r.Hosts {
		if h.Status == api.HostRemoved {
			continue
		}
		if activeOnly && h.Status != api.HostActive {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
