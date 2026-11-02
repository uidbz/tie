package api

// Access classifies who may call a route. It documents the auth model the
// server enforces; Phase-3 server wiring states it per handler.
type Access string

const (
	// AccessPublic needs no credential (the caller self-reports User for
	// job attribution only).
	AccessPublic Access = "public"
	// AccessDeployKey requires HTTP Basic auth whose password is the
	// DeployKey of the governing image (for pipelines: the entry step's).
	AccessDeployKey Access = "deploy-key"
	// AccessAdmin requires the shared AdminToken (Basic auth). Empty token
	// config disables these endpoints entirely.
	AccessAdmin Access = "admin-token"
	// AccessJoinToken requires the fleet seed's FleetJoinToken.
	AccessJoinToken Access = "join-token"
)

// Route is one endpoint of the runner API. Pattern uses net/http mux
// syntax; an empty method means any method (frozen for compatibility —
// prefer naming the method on new routes).
//
// Routes is the machine-readable endpoint table. It is pinned by
// testdata/runner/routes.json: a handler registered without a table entry,
// or a table entry no server registers, fails the fixture test in
// Phase-3 wiring; the JSON fixture additionally lets old clients verify
// they still speak the whole set.
type Route struct {
	Method  string // GET, POST, PUT, DELETE, or "" for any
	Pattern string // mux pattern, e.g. "/api/status/{jobid}"
	Access  Access
	Summary string
	Doc     string // section anchor in docs/runner-api.md
}

var Routes = []Route{
	{Method: "", Pattern: "/api/run", Access: AccessPublic, Summary: "submit a job (JobRequest to JobStatus); forwards between runners when routing", Doc: "run"},
	{Method: "", Pattern: "/api/status/{jobid}", Access: AccessPublic, Summary: "job status, forwarded to the owning backend", Doc: "status"},
	{Method: "GET", Pattern: "/api/jobs", Access: AccessPublic, Summary: "job list; query: since, user, status; no log field", Doc: "jobs"},
	{Method: "DELETE", Pattern: "/api/jobs/{jobid}", Access: AccessDeployKey, Summary: "cancel a job (key of its handler image)", Doc: "jobs"},
	{Method: "POST", Pattern: "/api/pipelines", Access: AccessPublic, Summary: "submit a whole step graph (PipelineRequest to Pipeline)", Doc: "pipelines"},
	{Method: "GET", Pattern: "/api/pipelines", Access: AccessPublic, Summary: "pipeline list; query: since, user, status", Doc: "pipelines"},
	{Method: "GET", Pattern: "/api/pipelines/{id}", Access: AccessPublic, Summary: "one pipeline", Doc: "pipelines"},
	{Method: "DELETE", Pattern: "/api/pipelines/{id}", Access: AccessDeployKey, Summary: "cancel a pipeline (key of its entry step)", Doc: "pipelines"},
	{Method: "POST", Pattern: "/api/pipelines/{id}/resume", Access: AccessDeployKey, Summary: "resume/rerun part of a run (ResumeRequest or ?from=)", Doc: "pipelines"},
	{Method: "GET", Pattern: "/api/queue", Access: AccessPublic, Summary: "queued jobs", Doc: "queue-active-running"},
	{Method: "GET", Pattern: "/api/active", Access: AccessPublic, Summary: "the single active job (back-compat shape)", Doc: "queue-active-running"},
	{Method: "GET", Pattern: "/api/running", Access: AccessPublic, Summary: "running jobs", Doc: "queue-active-running"},
	{Method: "", Pattern: "/api/deploymanager", Access: AccessDeployKey, Summary: "build and deploy an image (DeployRequest to DeployResponse); first deploy of a name needs no key", Doc: "deploy"},
	{Method: "", Pattern: "/api/filehost", Access: AccessPublic, Summary: "filehost URL + upload store (DeployResponse shape)", Doc: "filehost"},
	{Method: "GET", Pattern: "/api/fleet", Access: AccessPublic, Summary: "every runner URL in the fleet", Doc: "fleet"},
	{Method: "POST", Pattern: "/api/fleet/register", Access: AccessJoinToken, Summary: "fleet join handshake (seed only; idempotent polling)", Doc: "fleet-registry"},
	{Method: "GET", Pattern: "/api/fleet/hosts", Access: AccessAdmin, Summary: "pending/active hosts (non-seeds proxy to the seed)", Doc: "fleet-registry"},
	{Method: "POST", Pattern: "/api/fleet/approve", Access: AccessAdmin, Summary: "approve a pending host", Doc: "fleet-registry"},
	{Method: "POST", Pattern: "/api/fleet/remove", Access: AccessAdmin, Summary: "tombstone a host", Doc: "fleet-registry"},
	{Method: "POST", Pattern: "/api/cert", Access: AccessAdmin, Summary: "remote TLS cert install + hot swap, no restart", Doc: "cert"},
	{Method: "GET", Pattern: "/api/stats", Access: AccessPublic, Summary: "aggregate job history (JobStats); query: since, local", Doc: "stats"},
	{Method: "", Pattern: "/api/capabilities", Access: AccessPublic, Summary: "capabilities, resource pool, build info, cert expiry", Doc: "capabilities"},
	{Method: "POST", Pattern: "/api/reload", Access: AccessAdmin, Summary: "hot config reload (absent AdminToken disables the endpoint)", Doc: "reload"},
	{Method: "GET", Pattern: "/api/images", Access: AccessPublic, Summary: "deployed images (never DeployKeys)", Doc: "images"},
	{Method: "PUT", Pattern: "/api/images/replicate", Access: AccessAdmin, Summary: "push an image entry fleet-wide", Doc: "images"},
	{Method: "DELETE", Pattern: "/api/images/{image}", Access: AccessDeployKey, Summary: "delete an image", Doc: "images"},
	{Method: "GET", Pattern: "/api/services", Access: AccessPublic, Summary: "services keyed by name (TaskInfo)", Doc: "services"},
	{Method: "GET", Pattern: "/api/services/{service}", Access: AccessPublic, Summary: "one service", Doc: "services"},
	{Method: "POST", Pattern: "/api/services/{service}", Access: AccessDeployKey, Summary: "start a service", Doc: "services"},
	{Method: "DELETE", Pattern: "/api/services/{service}", Access: AccessDeployKey, Summary: "stop a service", Doc: "services"},
	{Method: "", Pattern: "/service/{service}", Access: AccessPublic, Summary: "data plane: prefix-stripping reverse proxy to the service container", Doc: "proxy"},
	{Method: "", Pattern: "/service/{service}/{params...}", Access: AccessPublic, Summary: "data plane with sub-paths; WebSocket tunneling", Doc: "proxy"},
	{Method: "GET", Pattern: "/files/", Access: AccessPublic, Summary: "static fileserver dir when enabled (client binaries, certs); config FileServer", Doc: "files"},
}
