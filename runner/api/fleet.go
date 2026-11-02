package api

import "encoding/json"

// CapabilityReply is the runner's self-report on GET-or-POST
// /api/capabilities: supported job types, the live resource pool (for
// capacity-aware fleet dispatch), build info, and health markers admins
// diff across the fleet.
type CapabilityReply struct {
	Capabilities []string
	IsAvailable  bool // back-compat: true when the runner has any free capacity
	QueueLength  int
	ActiveJobs   int
	// Resource pool for capacity-aware fleet dispatch.
	FreeCPU, FreeRAMMB, FreeGPU    int
	TotalCPU, TotalRAMMB, TotalGPU int
	// Build info so clients can report which runner revision they talk to.
	// Empty on runners built before these fields existed.
	Version   string
	GitCommit string
	BuildDate string
	// CertNotAfter is the configured TLS cert's expiry (RFC3339), empty when
	// no cert is set or it cannot be parsed. Fleet cert-expiry monitoring.
	CertNotAfter string
	// DeployedImageCount lets admins spot a runner that missed an image
	// replication: the count should match across the fleet.
	DeployedImageCount int
}

// FleetReply is the response of GET /api/fleet: every runner URL that makes
// up the fleet (the responding runner plus its configured backends). Clients
// use it to discover the whole fleet from a single seed URL.
type FleetReply struct {
	Servers []string
}

// Fleet host registry status values (GET /api/fleet/hosts,
// POST /api/fleet/approve|remove).
const (
	HostPending = "pending"
	HostActive  = "active"
	HostRemoved = "removed"
)

// RegisterRequest is the fleet join handshake sent by a new runner to the
// seed's POST /api/fleet/register. Re-registering the same URL is
// idempotent — that is how a joiner polls until an admin approves it.
type RegisterRequest struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Capabilities []string `json:"capabilities"`
	JoinToken    string   `json:"join_token"`
}

// RegisterReply answers the join handshake. Config carries the seed's
// runner configuration for the approved host (present only when Status is
// active). It is kept as raw JSON here because the config schema belongs to
// the server binary, not to this DTO package; its field set is pinned by
// testdata/runner/register-reply.json.
type RegisterReply struct {
	Status string          `json:"status"` // pending | active
	HostID string          `json:"host_id"`
	Config json.RawMessage `json:"config,omitempty"`
}

// FleetHost is one entry of the seed's fleet registry, as listed by
// GET /api/fleet/hosts (AdminToken gated). AdminToken is omitted unless the
// listing is meant to bootstrap a joiner's config.
type FleetHost struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Capabilities []string `json:"capabilities"`
	Status       string   `json:"status"` // pending | active | removed
	AdminToken   string   `json:"admin_token,omitempty"`
	Added        string   `json:"added"`
	Approved     string   `json:"approved,omitempty"`
}
