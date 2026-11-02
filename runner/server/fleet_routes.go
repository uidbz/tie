package server

// The fleet and admin surface: capabilities, the fleet listing, the
// seed's host-registry endpoints (non-seed runners proxy them to the
// seed), stats with the fleet merge, remote TLS cert push, and config
// reload.

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/sec"
)

func (s *Server) capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	snap := s.d.Sched.Snapshot()
	var caps []string
	if s.d.Capabilities != nil {
		caps = s.d.Capabilities()
	}
	resp := api.CapabilityReply{
		Capabilities:       caps,
		IsAvailable:        snap.FreeCPU > 0,
		QueueLength:        snap.QueueLength,
		ActiveJobs:         snap.Running,
		FreeCPU:            snap.FreeCPU,
		FreeRAMMB:          snap.FreeRAMMB,
		FreeGPU:            snap.FreeGPU,
		TotalCPU:           snap.TotalCPU,
		TotalRAMMB:         snap.TotalRAMMB,
		TotalGPU:           snap.TotalGPU,
		Version:            s.d.Version,
		GitCommit:          s.d.GitCommit,
		BuildDate:          s.d.BuildDate,
		CertNotAfter:       s.certNotAfter(),
		DeployedImageCount: len(s.d.Images.List()),
	}
	jsonResponse(w, resp)
}

func (s *Server) certNotAfter() string {
	if s.d.TLSCertPath == nil {
		return ""
	}
	return sec.CertNotAfter(s.d.TLSCertPath())
}

// fleetHandler advertises the whole fleet so a client that knows one
// runner can fan out to all of them without a hard-coded server list:
// this runner (derived from the request host) plus its backends.
func (s *Server) fleetHandler(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	var servers []string
	add := func(x string) {
		if x == "" || seen[x] {
			return
		}
		seen[x] = true
		servers = append(servers, x)
	}
	add(sec.GetBaseURL(r))
	if s.d.Backends != nil {
		for _, b := range s.d.Backends() {
			add(b)
		}
	}
	jsonResponse(w, api.FleetReply{Servers: servers})
}

// registerHandler is the seed-only installer endpoint. Auth: the
// deployment's fleet join token. Re-registering the same URL is
// idempotent, which is how a joining runner polls until an admin
// approves it.
func (s *Server) registerHandler(w http.ResponseWriter, r *http.Request) {
	joinToken := ""
	if s.d.FleetJoinToken != nil {
		joinToken = s.d.FleetJoinToken()
	}
	if joinToken == "" {
		http.Error(w, "not a fleet seed: no fleet join token configured", http.StatusNotFound)
		return
	}
	var req api.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" || req.Name == "" {
		http.Error(w, "name and url required", http.StatusBadRequest)
		return
	}
	if req.JoinToken != joinToken {
		http.Error(w, "bad join token", http.StatusUnauthorized)
		return
	}
	h := s.d.Fleet.AddPending(req.Name, req.URL, req.Capabilities)
	if err := s.d.Fleet.Save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep := api.RegisterReply{Status: h.Status, HostID: h.ID}
	if h.Status == api.HostActive && s.d.BuildHostConfig != nil {
		if cfg := s.d.BuildHostConfig(h); cfg != nil {
			raw, err := json.Marshal(cfg)
			if err != nil {
				http.Error(w, "cannot build host config: "+err.Error(), http.StatusInternalServerError)
				return
			}
			rep.Config = raw
		}
	}
	jsonResponse(w, rep)
}

// registryAdmin wraps a registry endpoint: admin auth first, then the
// seed answers from its own registry or proxies to the configured seed
// so any runner URL answers registry queries.
func (s *Server) registryAdmin(h http.HandlerFunc) http.HandlerFunc {
	return s.adminGate(func(w http.ResponseWriter, r *http.Request) {
		if !s.isSeed() {
			fleet.ForwardToSeed(nil, seedURL(s), w, r)
			return
		}
		h(w, r)
	})
}

func seedURL(s *Server) string {
	if s.d.FleetSeed == nil {
		return ""
	}
	return s.d.FleetSeed()
}

// fleetHostsHandler lists registry hosts (without minting tokens: the
// listing is for admins).
func (s *Server) fleetHostsHandler(w http.ResponseWriter, r *http.Request) {
	s.registryAdmin(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, s.d.Fleet.List(false))
	})(w, r)
}

// fleetApproveHandler approves a pending host by id and replies the
// approved entry (including its fresh admin token, which the admin
// hands to the host out-of-band, or the host picks up by
// re-registering).
func (s *Server) fleetApproveHandler(w http.ResponseWriter, r *http.Request) {
	s.registryAdmin(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		h := s.d.Fleet.Approve(req.ID)
		if h == nil {
			http.Error(w, "no pending host with id "+req.ID, http.StatusNotFound)
			return
		}
		if err := s.d.Fleet.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, h)
	})(w, r)
}

// fleetRemoveHandler tombstones a host.
func (s *Server) fleetRemoveHandler(w http.ResponseWriter, r *http.Request) {
	s.registryAdmin(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		h := s.d.Fleet.Remove(req.ID)
		if h == nil {
			http.Error(w, "no host with id "+req.ID, http.StatusNotFound)
			return
		}
		if err := s.d.Fleet.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, h)
	})(w, r)
}

// statsHandler returns aggregate statistics for this runner or the
// whole fleet. Query params: since (unix/duration/"30d", default 30d
// except stats default all-time), local=true to skip the fleet merge.
func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := int64(0)
	if sinceStr := q.Get("since"); sinceStr != "" {
		since = parseSince(sinceStr)
	}

	localStats, err := s.d.Store.Stats(since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Overlay live counts: the store's queued/running are stale after a
	// restart, the scheduler's snapshot is the truth for right now.
	snap := s.d.Sched.Snapshot()
	localStats.Running = snap.Running
	localStats.Queued = snap.QueueLength

	if q.Get("local") == "true" {
		jsonResponse(w, localStats)
		return
	}

	var remotes []*api.JobStats
	if s.d.Backends != nil {
		for _, backend := range s.d.Backends() {
			if remote, err := dispatch.FetchStats(nil, backend, since); err == nil {
				remotes = append(remotes, remote)
			}
		}
	}

	jsonResponse(w, dispatch.MergeStats(localStats, remotes...))
}

// certPushHandler validates and installs a new TLS keypair, hot-swaps
// it, and (on a runner that started plain HTTP) starts the TLS
// listener. Deployments renew certs without restarting, so long-running
// service containers never notice.
func (s *Server) certPushHandler(w http.ResponseWriter, r *http.Request) {
	s.adminGate(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cert string `json:"cert"` // PEM
			Key  string `json:"key"`  // PEM
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Cert == "" || req.Key == "" {
			http.Error(w, "cert and key (PEM) required", http.StatusBadRequest)
			return
		}
		certPath, keyPath := "", ""
		if s.d.TLSCertPath != nil {
			certPath = s.d.TLSCertPath()
		}
		if s.d.TLSKeyPath != nil {
			keyPath = s.d.TLSKeyPath()
		}
		if certPath == "" || keyPath == "" || s.d.Certs == nil {
			http.Error(w, "TLS cert/key paths not configured", http.StatusBadRequest)
			return
		}
		res, err := s.d.Certs.Install(req.Cert, req.Key, certPath, keyPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.d.OnCertInstalled != nil {
			s.d.OnCertInstalled()
		}
		jsonResponse(w, map[string]string{
			"status":         "installed",
			"subject":        res.Subject,
			"cert_not_after": res.NotAfter,
		})
	})(w, r)
}

// reloadHandler re-reads the deployment's config. The config schema and
// file belong to the binary; this endpoint just triggers it (SIGHUP is
// the other trigger) and reports the outcome.
func (s *Server) reloadHandler(w http.ResponseWriter, r *http.Request) {
	s.adminGate(func(w http.ResponseWriter, r *http.Request) {
		if s.d.Reload == nil {
			http.Error(w, "config reload not supported by this runner", http.StatusNotImplemented)
			return
		}
		if err := s.d.Reload(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("Config reloaded via POST /api/reload")
		jsonResponse(w, map[string]string{"status": "reloaded"})
	})(w, r)
}

// filehostHandler answers where clients upload: the filehost URL plus
// the configured store, so clients upload to the store the runner's
// jobs read from without duplicating the setting client-side.
func (s *Server) filehostHandler(w http.ResponseWriter, r *http.Request) {
	fh, st := "", ""
	if s.d.Filehost != nil {
		fh = s.d.Filehost()
	}
	if s.d.StoreName != nil {
		st = s.d.StoreName()
	}
	jsonResponse(w, api.DeployResponse{Message: fh, Store: st})
}
