package server

// The assembled server, end to end: real handlers, real scheduler,
// fake store and fake container runtime. These tests pin the HTTP
// contract the frozen route table promises.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/deploy"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/pipeline"
	"github.com/uidbz/tie/runner/scheduler"
	"github.com/uidbz/tie/runner/sec"
	"github.com/uidbz/tie/runner/service"
	"github.com/uidbz/tie/runner/store"
)

// fakeLocal runs jobs in memory: instant success, or a delay that Stop
// (and ctx cancel) cut short.
type fakeLocal struct {
	delay time.Duration

	mu      sync.Mutex
	stopped map[string]*stopOnce
}

type stopOnce struct {
	once sync.Once
	ch   chan struct{}
}

func (f *fakeLocal) Run(ctx context.Context, job *api.JobStatus, opts executor.RunOptions) executor.ExitInfo {
	name := executor.ContainerName(job.Handler)
	so := &stopOnce{ch: make(chan struct{})}
	f.mu.Lock()
	if f.stopped == nil {
		f.stopped = map[string]*stopOnce{}
	}
	f.stopped[name] = so
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.stopped, name)
		f.mu.Unlock()
	}()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
		case <-so.ch:
		}
	}
	return executor.ExitInfo{}
}

func (f *fakeLocal) Stop(containerName string) error {
	f.mu.Lock()
	so := f.stopped[containerName]
	f.mu.Unlock()
	if so == nil {
		return fmt.Errorf("no container %s", containerName)
	}
	so.once.Do(func() { close(so.ch) })
	return nil
}

type harnessCfg struct {
	images       []images.Image
	backends     []string
	capabilities []string
	totalCPU     int
	delay        time.Duration
	joinToken    string
	seedURL      string
	extDir       string
	hostConfig   func(*api.FleetHost) any
	dockerBin    string
	filehost     string
}

type harness struct {
	t        *testing.T
	srv      *httptest.Server
	st       *store.Fake
	sched    *scheduler.Manager
	images   *images.Registry
	fleetReg *fleet.Registry
	local    *fakeLocal
	svc      *service.Manager
	certs    *sec.CertManager

	reloads    int
	certPath   string
	keyPath    string
	filehost   string
	storeName  string
	adminToken string
	extDir     string
}

func newHarness(t *testing.T, cfg harnessCfg) *harness {
	t.Helper()
	h := &harness{t: t, adminToken: "admintok", storeName: "tie", extDir: cfg.extDir, filehost: cfg.filehost}
	if cfg.extDir == "" {
		cfg.extDir = t.TempDir()
		h.extDir = cfg.extDir
	}
	h.st = store.NewFake()
	h.local = &fakeLocal{delay: cfg.delay}
	h.images = images.New(func([]images.Image) error { return nil }, cfg.images...)
	h.fleetReg = fleet.Load(filepath.Join(t.TempDir(), "fleet-registry.json"))
	h.certs = &sec.CertManager{}
	h.svc = service.New(service.Config{
		Images: h.images, Bin: "docker-does-not-exist",
		ExtDir: cfg.extDir, Ports: service.NewPortPool(3000, 3100),
	})

	if cfg.totalCPU == 0 {
		cfg.totalCPU = 4
	}
	caps := cfg.capabilities
	if caps == nil {
		caps = []string{"CPUv1"}
	}

	var srv *Server
	pipe := pipeline.New(pipeline.Config{
		Store:        h.st,
		Filehost:     cfg.filehost,
		StoreName:    h.storeName,
		SubmitJob:    func(req api.JobRequest, dm string) (*api.JobStatus, error) { return srv.submitJob(req, dm) },
		CancelJob:    func(id string) error { return srv.d.Sched.Cancel(id) },
		ValidateStep: func(req api.JobRequest) error { return srv.validateJobRequest(req) },
	})
	h.sched = scheduler.New(scheduler.Config{
		Local: h.local, Store: h.st,
		TotalCPU: cfg.totalCPU, TotalRAMMB: 8192,
		ResolveSpec: func(jt string) (executor.Spec, bool) {
			if jt == "CPUv1" {
				return executor.Spec{CPU: 1, RAMMB: 512}, true
			}
			return executor.Spec{}, false
		},
		OnFinished: pipe.JobFinished,
	})

	srv = New(Deps{
		Store: h.st, Sched: h.sched, Pipe: pipe, Svc: h.svc,
		Images: h.images, Auth: &sec.Auth{}, Certs: h.certs,
		Fleet: h.fleetReg,
		Dispatch: &dispatch.Router{
			Capabilities: func() []string { return caps },
			Backends:     func() []string { return cfg.backends },
			ResolveSpec:  h.sched.SpecFor,
			CanFit:       h.sched.CanFit,
		},
		Filehost:        func() string { return h.filehost },
		StoreName:       func() string { return h.storeName },
		AdminToken:      func() string { return h.adminToken },
		FleetJoinToken:  func() string { return cfg.joinToken },
		FleetSeed:       func() string { return cfg.seedURL },
		Backends:        func() []string { return cfg.backends },
		Capabilities:    func() []string { return caps },
		ExtDir:          func() string { return cfg.extDir },
		TLSCertPath:     func() string { return h.certPath },
		TLSKeyPath:      func() string { return h.keyPath },
		Version:         "v9.9.9",
		GitCommit:       "abc123",
		BuildDate:       "1970-01-01",
		Reload:          func() error { h.reloads++; return nil },
		BuildHostConfig: cfg.hostConfig,
		Builder: &deploy.Builder{
			Bin:      orElse(cfg.dockerBin, "docker-does-not-exist"),
			Registry: func() string { return "registry.invalid" },
			Filehost: func() string { return h.filehost },
		},
	})
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (h *harness) do(method, path, body string, user, pass string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	// Incoming server requests carry RequestURI (the forwarding paths
	// replay it); http.NewRequest leaves it empty.
	req.RequestURI = req.URL.RequestURI()
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	rec := httptest.NewRecorder()
	h.srv.Config.Handler.ServeHTTP(rec, req)
	return rec
}

const goodHash = "0000000000000000000000000000000000000000000000000000000000000abc"

func jobReqJSON(handler string) string {
	return fmt.Sprintf(`{"JobType":"CPUv1","Handler":%q,"InputHash":%q}`, handler, goodHash)
}

// waitFor polls GET /api/status until cond(reply) holds or the deadline
// passes.
func (h *harness) waitFor(jobID string, cond func(*api.JobStatus) bool) *api.JobStatus {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last api.JobStatus
	for time.Now().Before(deadline) {
		rec := h.do(http.MethodGet, "/api/status/"+jobID, "", "", "")
		json.Unmarshal(rec.Body.Bytes(), &last)
		if cond(&last) {
			return &last
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("job %s never reached the expected state (last: %+v)", jobID, last)
	return nil
}

func TestJobLifecycle(t *testing.T) {
	h := newHarness(t, harnessCfg{images: []images.Image{{Name: "tool:v2", DeployKey: "k"}}})

	rec := h.do(http.MethodPost, "/api/run", jobReqJSON("tool:v2"), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var created api.JobStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.JobID == "" || created.DeployManager != h.srv.URL {
		t.Errorf("created job identity: %+v", created)
	}

	h.waitFor(created.JobID, func(j *api.JobStatus) bool { return j.Done })

	rec = h.do(http.MethodGet, "/api/jobs", "", "", "")
	var jobs []*api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &jobs)
	if len(jobs) != 1 || jobs[0].JobID != created.JobID {
		t.Errorf("jobs list: %+v", jobs)
	}

	rec = h.do(http.MethodGet, "/api/running", "", "", "")
	if rec.Body.String() != "[]\n" {
		t.Errorf("running after completion: %s", rec.Body.String())
	}
	rec = h.do(http.MethodGet, "/api/active", "", "", "")
	var active api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &active)
	if active.JobID != "" {
		t.Errorf("active when idle: %+v", active)
	}

	// Cancelling a finished job hits the DeployKey gate first (the live
	// record lingers for a week), then 404: nothing queued or running.
	if rec := h.do(http.MethodDelete, "/api/jobs/"+created.JobID, "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("cancel finished job without auth: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/jobs/"+created.JobID, "", "tie", "k"); rec.Code != http.StatusNotFound {
		t.Errorf("cancel finished job: %d", rec.Code)
	}
}

func TestJobValidation(t *testing.T) {
	h := newHarness(t, harnessCfg{})

	rec := h.do(http.MethodPost, "/api/run", `{"JobType":"CPUv1","Handler":"tool:v2","InputHash":"short"}`, "", "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid hash (wrong length)") {
		t.Errorf("short hash: %d %s", rec.Code, rec.Body.String())
	}

	// A job type nobody in the fleet is capable of: dispatch rejects
	// before validation (historic reply: error body, HTTP 200).
	rec = h.do(http.MethodPost, "/api/run", `{"JobType":"GHOST","Handler":"tool:v2","InputHash":"`+goodHash+`"}`, "", "")
	if !strings.Contains(rec.Body.String(), "No suitable backends!") {
		t.Errorf("uncapable type: %d %s", rec.Code, rec.Body.String())
	}
}

func TestJobCancelAuthAndFlow(t *testing.T) {
	h := newHarness(t, harnessCfg{
		delay:  3 * time.Second,
		images: []images.Image{{Name: "slow:latest", DeployKey: "k1"}},
	})

	rec := h.do(http.MethodPost, "/api/run", jobReqJSON("slow:latest"), "", "")
	var created api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &created)

	// Queued/running: wrong or missing DeployKey must not cancel.
	if rec := h.do(http.MethodDelete, "/api/jobs/"+created.JobID, "", "tie", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("cancel wrong key: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/jobs/"+created.JobID, "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("cancel no auth: %d", rec.Code)
	}

	h.waitFor(created.JobID, func(j *api.JobStatus) bool { return j.Status == api.StatusRunning })

	if rec := h.do(http.MethodDelete, "/api/jobs/"+created.JobID, "", "tie", "k1"); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	j := h.waitFor(created.JobID, func(j *api.JobStatus) bool { return j.Done })
	if j.Status != api.StatusCancelled {
		t.Errorf("final status: %+v", j)
	}
}

func TestQueueAndCapacityEndpoints(t *testing.T) {
	h := newHarness(t, harnessCfg{delay: 2 * time.Second})

	ids := map[string]bool{}
	// Two different handlers: same-handler jobs serialize, same as the
	// real runner.
	for _, handler := range []string{"tool:v2", "other:v1"} {
		rec := h.do(http.MethodPost, "/api/run", jobReqJSON(handler), "", "")
		var c api.JobStatus
		json.Unmarshal(rec.Body.Bytes(), &c)
		ids[c.JobID] = true
	}

	// One runs, one waits (fake local has no capacity limit; both run).
	// Both should show running quickly.
	deadline := time.Now().Add(5 * time.Second)
	var running []*api.JobStatus
	for time.Now().Before(deadline) {
		rec := h.do(http.MethodGet, "/api/running", "", "", "")
		json.Unmarshal(rec.Body.Bytes(), &running)
		if len(running) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(running) != 2 {
		t.Fatalf("expected 2 running jobs, got %d", len(running))
	}

	rec := h.do(http.MethodGet, "/api/active", "", "", "")
	var active api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &active)
	if !ids[active.JobID] {
		t.Errorf("active job not one of ours: %+v", active)
	}

	rec = h.do(http.MethodGet, "/api/queue", "", "", "")
	if rec.Body.String() != "[]\n" {
		t.Errorf("queue: %s", rec.Body.String())
	}

	rec = h.do(http.MethodGet, "/api/stats?local=true", "", "", "")
	var stats api.JobStats
	json.Unmarshal(rec.Body.Bytes(), &stats)
	if stats.Running != 2 {
		t.Errorf("stats.Running = %d, want 2 (snapshot overlay)", stats.Running)
	}
	if stats.Total != 2 {
		t.Errorf("store stats total: %+v", stats)
	}
}
