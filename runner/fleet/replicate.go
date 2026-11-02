package fleet

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/runner/images"
)

// Replicator pushes deployed-image entries to every backend runner when
// an image is deployed here, so any runner that later executes a job for
// that image can authorize cancel/stop. Failures are warnings only —
// dispatch already degrades gracefully when a backend is down, and the
// local entry is always kept.
type Replicator struct {
	// Client is nil-safe (55s timeout default).
	Client *http.Client
	// Backends/AdminUser/AdminToken are accessors so a hot-reloaded
	// config takes effect on the next replication. An empty admin token
	// disables replication (with a warning).
	Backends   func() []string
	AdminUser  func() string
	AdminToken func() string

	wg sync.WaitGroup
}

// ReplicateImage fans the image entry out to all backends without
// blocking the deploy that triggered it. Wait blocks until every in
// flight replication finished (graceful shutdown uses it).
func (r *Replicator) ReplicateImage(img images.Image) {
	token := ""
	if r.AdminToken != nil {
		token = r.AdminToken()
	}
	if token == "" {
		slog.Warn("Image replication skipped: no admin token configured", "name", img.Name)
		return
	}
	user := "tie"
	if r.AdminUser != nil {
		user = r.AdminUser()
	}
	var backends []string
	if r.Backends != nil {
		backends = r.Backends()
	}
	body, err := json.Marshal(img)
	if err != nil {
		slog.Error("Image replication: cannot encode entry", "name", img.Name, "error", err)
		return
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 55 * time.Second}
	}
	for _, backend := range backends {
		r.wg.Add(1)
		go func(backend string) {
			defer r.wg.Done()
			url := strings.TrimRight(backend, "/") + "/api/images/replicate"
			req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
			if err != nil {
				slog.Warn("Image replication: bad backend URL", "backend", backend, "error", err)
				return
			}
			req.SetBasicAuth(user, token)
			resp, err := client.Do(req)
			if err != nil {
				slog.Warn("Image replication: backend unreachable", "backend", backend, "name", img.Name, "error", err)
				return
			}
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if resp.StatusCode != http.StatusOK {
				slog.Warn("Image replication rejected", "backend", backend, "name", img.Name,
					"status", resp.StatusCode, "body", strings.TrimSpace(string(respBody)))
				return
			}
			slog.Info("Image entry replicated", "backend", backend, "name", img.Name)
		}(backend)
	}
}

// Wait blocks until all in-flight replications complete.
func (r *Replicator) Wait() { r.wg.Wait() }

// ForwardToSeed answers the current request by forwarding its method,
// path, body, and Basic Auth to the seed runner's same path (the seed
// checks its own admin token). Used by non-seed hosts so any runner URL
// answers registry queries.
func ForwardToSeed(client *http.Client, seed string, w http.ResponseWriter, r *http.Request) {
	if client == nil {
		client = http.DefaultClient
	}
	if seed == "" {
		http.Error(w, "not a fleet seed and no seed URL is configured", http.StatusNotFound)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(seed, "/")+r.URL.Path, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if u, p, ok := r.BasicAuth(); ok {
		req.SetBasicAuth(u, p)
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "seed unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
