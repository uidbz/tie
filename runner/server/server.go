// Package server assembles a runner's HTTP surface from the engine
// packages: the frozen route table wired to the scheduler, pipeline
// sequencer, service runtime, image registry, fleet machinery, and
// access control. The binary around it owns only configuration and
// process lifecycle; everything protocol-shaped lives here.
//
// Configuration values are read through accessors (not snapshots) so
// hot reload takes effect on the next request. Handlers whose behavior
// depends on deployment-specific pieces (config reload, seed config
// generation, external backends) receive them as optional hooks and
// answer with a clear error when the hook is absent.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/deploy"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/pipeline"
	"github.com/uidbz/tie/runner/proxy"
	"github.com/uidbz/tie/runner/scheduler"
	"github.com/uidbz/tie/runner/sec"
	"github.com/uidbz/tie/runner/service"
	"github.com/uidbz/tie/runner/store"
)

// Deps is everything the HTTP surface needs. The engine pointers are
// required; the function fields are deployment hooks, nil-safe.
type Deps struct {
	Store      store.Store
	Sched      *scheduler.Manager
	Pipe       *pipeline.Engine
	Svc        *service.Manager
	Images     *images.Registry
	Dispatch   *dispatch.Router
	Auth       *sec.Auth
	Certs      *sec.CertManager
	Fleet      *fleet.Registry
	Replicator *fleet.Replicator
	Builder    *deploy.Builder // nil disables the deploy endpoint
	// Proxy serves the /service/* data plane; nil answers "not running".
	Proxy *proxy.Handler

	// Config accessors, read per request so reloads apply immediately.
	Filehost       func() string
	StoreName      func() string
	AdminToken     func() string
	FleetJoinToken func() string // non-empty marks this runner as a fleet seed
	FleetSeed      func() string // seed URL to forward registry ops to
	Backends       func() []string
	Capabilities   func() []string
	// ExtDir is the host-side scratch base deploys create per image.
	ExtDir func() string
	// TLSCertPath feeds the cert-expiry capability; TLSKeyPath gates the
	// cert push endpoint alongside Certs.
	TLSCertPath func() string
	TLSKeyPath  func() string

	// Version triple stamped into /api/capabilities (set via ldflags).
	Version   string
	GitCommit string
	BuildDate string

	// Hooks:
	// ImageCheck rejects submissions whose image is gone from the
	// registry (imagecheck.Check with the deployment's registry bound).
	ImageCheck func(handler string) error
	// ExtCapable reports job types this runner delegates to an external
	// backend instead of the local queue.
	ExtCapable func(jobType string) bool
	// BuildHostConfig answers the register handshake with the config
	// document an approved host should run (seed only; the config schema
	// belongs to the binary).
	BuildHostConfig func(h *api.FleetHost) any
	// Reload re-reads the deployment's config and re-applies it.
	Reload func() error
	// OnCertInstalled starts the TLS listener the first time a cert is
	// pushed to a runner that started plain HTTP.
	OnCertInstalled func()
}

// Server is a wired route table.
type Server struct {
	d   Deps
	mux *http.ServeMux
}

// New wires Deps into the frozen route table.
func New(d Deps) *Server {
	s := &Server{d: d, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler is the assembled mux. Deployments that want more (a default
// route of their own, extra mounts) wrap this in their own mux.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Control plane: all management lives under /api so it can never be
	// shadowed by the /service/* data-plane reverse proxy.
	s.mux.HandleFunc("/api/run", s.runHandler)
	s.mux.HandleFunc("/api/status/{jobid}", s.jobStatusHandler)
	s.mux.HandleFunc("GET /api/jobs", s.jobsHandler)
	s.mux.HandleFunc("DELETE /api/jobs/{jobid}", s.cancelJobHandler)
	// Pipelines: the runner sequences a whole step graph so the client
	// can exit.
	s.mux.HandleFunc("POST /api/pipelines", s.submitPipelineHandler)
	s.mux.HandleFunc("GET /api/pipelines", s.pipelinesHandler)
	s.mux.HandleFunc("GET /api/pipelines/{id}", s.pipelineStatusHandler)
	s.mux.HandleFunc("DELETE /api/pipelines/{id}", s.cancelPipelineHandler)
	s.mux.HandleFunc("POST /api/pipelines/{id}/resume", s.resumePipelineHandler)
	s.mux.HandleFunc("GET /api/queue", s.queueHandler)
	s.mux.HandleFunc("GET /api/active", s.activeHandler)
	s.mux.HandleFunc("GET /api/running", s.runningHandler)
	s.mux.HandleFunc("/api/deploymanager", s.deployHandler)
	s.mux.HandleFunc("/api/filehost", s.filehostHandler)
	s.mux.HandleFunc("GET /api/fleet", s.fleetHandler)
	// Fleet host registry (seed side; non-seed runners proxy to the seed).
	s.mux.HandleFunc("POST /api/fleet/register", s.registerHandler)
	s.mux.HandleFunc("GET /api/fleet/hosts", s.fleetHostsHandler)
	s.mux.HandleFunc("POST /api/fleet/approve", s.fleetApproveHandler)
	s.mux.HandleFunc("POST /api/fleet/remove", s.fleetRemoveHandler)
	s.mux.HandleFunc("POST /api/cert", s.certPushHandler)
	s.mux.HandleFunc("GET /api/stats", s.statsHandler)
	s.mux.HandleFunc("/api/capabilities", s.capabilitiesHandler)
	s.mux.HandleFunc("POST /api/reload", s.reloadHandler)
	s.mux.HandleFunc("GET /api/images", s.deployedImagesHandler)
	s.mux.HandleFunc("PUT /api/images/replicate", s.replicateImageHandler)
	s.mux.HandleFunc("DELETE /api/images/{image}", s.d.Auth.DeployKeyGate(s.d.Images)(s.deleteImageHandler))
	s.mux.HandleFunc("GET /api/services", s.listServicesHandler)
	s.mux.HandleFunc("GET /api/services/{service}", s.taskStatusHandler)
	s.mux.HandleFunc("POST /api/services/{service}", s.d.Auth.DeployKeyGate(s.d.Images)(s.startServiceHandler))
	s.mux.HandleFunc("DELETE /api/services/{service}", s.d.Auth.DeployKeyGate(s.d.Images)(s.stopTaskHandler))
	// Data plane: reverse proxy to running service containers.
	s.mux.HandleFunc("/service/{service}", s.serviceHandler)
	s.mux.HandleFunc("/service/{service}/{params...}", s.serviceHandler)
}

// ─────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("Failed to encode JSON response", "error", err)
	}
}

// errorResponse answers with the zero JobStatus carrying only ErrorMsg:
// the historic error body of the run/status family (clients parse that
// shape, so it stays).
func errorResponse(w http.ResponseWriter, errorMsg string) {
	resp := &api.JobStatus{ErrorMsg: errorMsg}
	jsonResponse(w, resp)
}

// parseSince turns a since query value into a unix cutoff: raw unix
// seconds, a Go duration, "Nd" day counts, or the 30-day default.
func parseSince(s string) int64 {
	if s == "" {
		return time.Now().Add(-30 * 24 * time.Hour).Unix()
	}
	if strings.HasSuffix(s, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d).Unix()
	}
	if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
		return ts
	}
	return time.Now().Add(-30 * 24 * time.Hour).Unix()
}

func (s *Server) isSeed() bool {
	return s.d.FleetJoinToken != nil && s.d.FleetJoinToken() != ""
}

// adminGate is the Basic Auth gate for the admin surface (fleet
// registry, cert push, reload).
func (s *Server) adminGate(h http.HandlerFunc) http.HandlerFunc {
	return s.d.Auth.AdminGate(s.d.AdminToken)(h)
}
