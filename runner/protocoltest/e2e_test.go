package protocoltest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/pipeline"
	"github.com/uidbz/tie/runner/proxy"
	"github.com/uidbz/tie/runner/scheduler"
	"github.com/uidbz/tie/runner/sec"
	"github.com/uidbz/tie/runner/server"
	"github.com/uidbz/tie/runner/service"
	"github.com/uidbz/tie/runner/store"
)

// fakeFilehost serves GET /{hash} from a blob map and PUT stores the
// body under its computed content address, echoing the address back —
// the shape compose.Build's uploads expect.
func fakeFilehost(t *testing.T, blobs map[string]string) string {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			hash := strings.TrimPrefix(r.URL.Path, "/")
			mu.Lock()
			body, ok := blobs[hash]
			mu.Unlock()
			if !ok {
				http.Error(w, "no such blob", http.StatusNotFound)
				return
			}
			io.WriteString(w, body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			addr, err := metadata.HashReader(strings.NewReader(string(body)))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			blobs[addr] = string(body)
			mu.Unlock()
			io.WriteString(w, addr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeLocal stands in for the container runtime: jobs "run" for delay,
// honour Stop (user cancellation), and report their result through the
// hooks sink like the real runtime does — the scheduler therefore sees
// identical job records either way.
type fakeLocal struct {
	mu      sync.Mutex
	delay   time.Duration
	stopped map[string]*stopCh
	// result is the tree hash every fake job reports, pre-seeded on the
	// fake filehost so pipeline steps can consume it.
	result string
}

func (f *fakeLocal) setDelay(d time.Duration) {
	f.mu.Lock()
	f.delay = d
	f.mu.Unlock()
}

func (f *fakeLocal) runDelay() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.delay
}

func (f *fakeLocal) Run(ctx context.Context, job *api.JobStatus, opts executor.RunOptions) executor.ExitInfo {
	name := executor.ContainerName(job.Handler)
	ch := make(chan struct{})
	f.mu.Lock()
	if f.stopped == nil {
		f.stopped = map[string]*stopCh{}
	}
	sc := &stopCh{ch: ch}
	f.stopped[name] = sc
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.stopped, name)
		f.mu.Unlock()
	}()
	if d := f.runDelay(); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		case <-sc.ch:
		}
	}
	if opts.Hooks.Result != nil && f.result != "" {
		opts.Hooks.Result(f.result)
	}
	return executor.ExitInfo{}
}

type stopCh struct {
	once sync.Once
	ch   chan struct{}
}

func (f *fakeLocal) Stop(containerName string) error {
	f.mu.Lock()
	sc := f.stopped[containerName]
	f.mu.Unlock()
	if sc == nil {
		return fmt.Errorf("no container %s", containerName)
	}
	sc.once.Do(func() { close(sc.ch) })
	return nil
}

func hashOf(t *testing.T, content string) string {
	t.Helper()
	addr, err := metadata.HashReader(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// uploadTree puts a one-file directory tree on the fake filehost (via
// the real manifest upload path) and returns its tree hash — the shape
// job inputs and outputs always have.
func uploadTree(t *testing.T, filehost, name, content string) string {
	t.Helper()
	b := compose.NewTreeBuilder()
	if err := b.Add(name, metadata.DirEntry{Hash: hashOf(t, content), Filename: name, Size: len(content)}); err != nil {
		t.Fatal(err)
	}
	addr, err := b.Upload(filehost, "")
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// fixture is the assembled runner behind a real HTTP listener: the same
// exported composition cmd/tie-runner performs, with only the durable
// store and the container runtime faked.
type fixture struct {
	url        string
	local      *fakeLocal
	images     *images.Registry
	filehost   string
	blobs      map[string]string
	resultHash string // the tree every fake job reports as its output
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	blobs := map[string]string{}
	fhURL := fakeFilehost(t, blobs)
	// The fake runtime reports resultHash as every job's output; pre-
	// seed that tree so pipeline stepping can consume it exactly like a
	// real job's uploaded output.
	resultHash := uploadTree(t, fhURL, "out.dat", "job output")

	st := store.NewFake()
	local := &fakeLocal{result: resultHash}
	imgReg := images.New(func([]images.Image) error { return nil },
		images.Image{Name: "tool:v2", DeployKey: "dk"})
	svc := service.New(service.Config{
		Images: imgReg, Bin: "docker-does-not-exist",
		ExtDir: t.TempDir(), Ports: service.NewPortPool(3000, 3100),
	})

	var sched *scheduler.Manager
	var srv *server.Server
	pipe := pipeline.New(pipeline.Config{
		Store:     st,
		Filehost:  fhURL,
		StoreName: "",
		SubmitJob: func(req api.JobRequest, dm string) (*api.JobStatus, error) {
			return srv.SubmitJob(req, dm)
		},
		CancelJob: func(jobID string) error { return sched.Cancel(jobID) },
		ValidateStep: func(req api.JobRequest) error {
			return srv.ValidateStep(req)
		},
	})

	sched = scheduler.New(scheduler.Config{
		Local:      local,
		Store:      st,
		TotalCPU:   8,
		TotalRAMMB: 65536,
		TotalGPU:   2,
		Filehost:   fhURL,
		OnFinished: pipe.JobFinished,
		OnStart: func(job *api.JobStatus) {
			svc.RegisterTask(job.Handler, api.TaskTypeJob)
			svc.Activate(job.Handler, api.TaskTypeJob, 0)
		},
		OnEnd: func(job *api.JobStatus) {
			svc.Deactivate(job.Handler)
			svc.UnregisterTask(job.Handler)
		},
	})

	srv = server.New(server.Deps{
		Store:  st,
		Sched:  sched,
		Pipe:   pipe,
		Svc:    svc,
		Images: imgReg,
		Auth:   &sec.Auth{}, // default auth user: "tie", the runclient default
		Fleet:  fleet.Load(filepath.Join(t.TempDir(), "fleet-registry.json")),
		Dispatch: &dispatch.Router{
			Capabilities: func() []string { return []string{"CPUv1"} },
			ResolveSpec:  sched.SpecFor,
			CanFit:       sched.CanFit,
		},
		Proxy:        proxy.New(proxy.Config{ExternalPort: svc.RunningPort}),
		Filehost:     func() string { return fhURL },
		StoreName:    func() string { return "" },
		Capabilities: func() []string { return []string{"CPUv1"} },
		Version:      "v-test",
		GitCommit:    "testcommit",
		BuildDate:    "1970-01-01",
	})

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &fixture{url: ts.URL, local: local, images: imgReg, filehost: fhURL, blobs: blobs, resultHash: resultHash}
}

// TestRunWatchHistory pins the core client/server dialogue over the real
// transport: submit, watch to completion, then read the result back out
// of the history and fleet-wide list surfaces.
func TestRunWatchHistory(t *testing.T) {
	f := newFixture(t)
	in := hashOf(t, "input content")

	created, err := runclient.Run(f.url, api.JobRequest{
		JobType: "CPUv1", Handler: "tool:v2", InputHash: in,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.JobID == "" {
		t.Fatal("no job id on submission")
	}
	if created.User == "" {
		t.Error("client did not fill the submitting user")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	final, err := runclient.ShowProgressCtx(ctx, f.url, created.JobID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Done || final.Status != api.StatusDone {
		t.Errorf("final status = %s (done=%v), want done", final.Status, final.Done)
	}
	if final.OutputHash != f.resultHash {
		t.Errorf("OutputHash = %q, want the reported result", final.OutputHash)
	}
	if final.Handler != "tool:v2" || final.JobType != "CPUv1" || final.InputHash != in {
		t.Errorf("record drifted through the wire: %+v", final)
	}

	// History and fleet-wide lists must show the same record.
	jobs, errs := runclient.ListJobsAll([]string{f.url}, time.Hour, "", "")
	if len(errs) > 0 {
		t.Fatalf("list errors: %v", errs)
	}
	if len(jobs) != 1 || jobs[0].Value.JobID != created.JobID {
		t.Fatalf("history = %+v", jobs)
	}
	if jobs[0].Server != f.url {
		t.Errorf("fan-out tag = %q, want %q", jobs[0].Server, f.url)
	}

	queue, errs := runclient.QueueAll([]string{f.url})
	if len(errs) > 0 || len(queue[f.url]) != 0 {
		t.Errorf("queue after completion: %v %v", queue, errs)
	}
	active, errs := runclient.ActiveAll([]string{f.url})
	if len(errs) > 0 || len(active[f.url]) != 0 {
		t.Errorf("active after completion: %v %v", active, errs)
	}
}

// TestActiveDuringRunAndFilehostSurfaces checks the live views while a
// job runs, and the static endpoints that carry deployment identity.
func TestActiveDuringRunAndFilehostSurfaces(t *testing.T) {
	f := newFixture(t)
	f.local.setDelay(400 * time.Millisecond)

	created, err := runclient.Run(f.url, api.JobRequest{
		JobType: "CPUv1", Handler: "tool:v2", InputHash: hashOf(t, "x"),
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := runclient.GetStatus(f.url, created.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status == api.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reached running: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}

	active, errs := runclient.ActiveAll([]string{f.url})
	if len(errs) > 0 {
		t.Fatalf("active errors: %v", errs)
	}
	if len(active[f.url]) != 1 || active[f.url][0].JobID != created.JobID {
		t.Errorf("active = %+v, want the running job", active[f.url])
	}

	fh, fhStore, err := runclient.GetFilehostStore(f.url)
	if err != nil {
		t.Fatal(err)
	}
	if fh != f.filehost || fhStore != "" {
		t.Errorf("filehost surface = %q/%q, want %q/\"\"", fh, fhStore, f.filehost)
	}

	caps, err := runclient.GetVersion(f.url)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Version != "v-test" || caps.GitCommit != "testcommit" {
		t.Errorf("build stamp = %+v", caps)
	}
	if len(caps.Capabilities) != 1 || caps.Capabilities[0] != "CPUv1" {
		t.Errorf("capabilities = %v", caps.Capabilities)
	}
	if caps.TotalCPU != 8 || caps.TotalRAMMB != 65536 {
		t.Errorf("capacity stamp = %+v", caps)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := runclient.ShowProgressCtx(ctx, f.url, created.JobID, true, true); err != nil {
		t.Fatal(err)
	}
}

// TestCancelAuthorization pins the cancel path end to end: a running
// job cancels only with the handler image's DeployKey, and the reply
// codes distinguish "wrong key" from "already finished".
func TestCancelAuthorization(t *testing.T) {
	f := newFixture(t)
	f.local.setDelay(30 * time.Second)

	created, err := runclient.Run(f.url, api.JobRequest{
		JobType: "CPUv1", Handler: "tool:v2", InputHash: hashOf(t, "x"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until it is actually running before cancelling.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := runclient.GetStatus(f.url, created.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status == api.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reached running: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wrong key first: the gate must reject it while the job lives.
	if err := runclient.CancelJob(f.url, created.JobID, "wrong"); !errors.Is(err, runclient.ErrUnauthorized) {
		t.Errorf("wrong-key cancel = %v, want ErrUnauthorized", err)
	}
	if err := runclient.CancelJob(f.url, created.JobID, "dk"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	st, err := runclient.GetStatus(f.url, created.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done || st.Status != api.StatusCancelled {
		t.Errorf("after cancel: status = %s (done=%v), want cancelled", st.Status, st.Done)
	}

	// Cancelling a finished job: right key now 404s (nothing to cancel),
	// wrong key stays a 401.
	if err := runclient.CancelJob(f.url, created.JobID, "dk"); !errors.Is(err, runclient.ErrNotFound) {
		t.Errorf("cancel of finished job = %v, want ErrNotFound", err)
	}
	if err := runclient.CancelJob(f.url, created.JobID, "wrong"); !errors.Is(err, runclient.ErrUnauthorized) {
		t.Errorf("wrong-key cancel of finished job = %v, want ErrUnauthorized", err)
	}
}

// TestPipelineRoundTrip drives the sequencer through both halves: the
// client submits a seed-fed graph, the server composes each step's
// input against the fake filehost and submits real job records, and the
// client waits the run out and reads the finished graph back.
func TestPipelineRoundTrip(t *testing.T) {
	f := newFixture(t)

	seed := uploadTree(t, f.filehost, "in.dat", "seed content")
	created, err := runclient.SubmitPipeline(f.url, api.PipelineRequest{
		Seeds: map[string]string{"raw": seed},
		Steps: []api.PipelineStep{
			{
				Name:    "one",
				Handler: "tool:v2",
				JobType: "CPUv1",
				Inputs:  []compose.Spec{{FromStep: "raw"}},
			},
			{
				Name:    "two",
				Handler: "tool:v2",
				JobType: "CPUv1",
				Inputs:  []compose.Spec{{FromStep: "one"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PipelineID == "" {
		t.Fatalf("submission = %+v", created)
	}
	// Seeds share the step namespace, so the graph carries the two real
	// steps plus the seed entry.
	for _, name := range []string{"one", "two"} {
		if runclient.Step(&created, name) == nil {
			t.Fatalf("step %q missing from submission", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stepNames []string
	p, err := runclient.WaitPipelineCtx(ctx, f.url, created.PipelineID, 20*time.Millisecond,
		func(s *api.PipelineStep) { stepNames = append(stepNames, s.Name+":"+s.Status) })
	if err != nil {
		t.Fatal(err)
	}
	if !runclient.PipelineDone(&p) {
		t.Fatalf("pipeline not done: %+v", p)
	}
	for _, name := range []string{"one", "two"} {
		s := runclient.Step(&p, name)
		if s == nil {
			t.Fatalf("step %q missing", name)
		}
		if s.Status != api.StatusDone || s.JobID == "" || s.OutputHash != f.resultHash {
			t.Errorf("step %q = %+v", name, s)
		}
		// The step's job is a first-class record on its own.
		if js, err := runclient.GetStatus(f.url, s.JobID); err != nil || js.Status != api.StatusDone {
			t.Errorf("step %q job %s: %+v %v", name, s.JobID, js, err)
		}
	}
	if runclient.EntryHandler(&p) != "tool:v2" {
		t.Errorf("entry handler = %q", runclient.EntryHandler(&p))
	}

	// Fleet-wide lookup finds it and tags the owning server.
	found, err := runclient.FindPipeline([]string{f.url}, created.PipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Server != f.url || found.Value.PipelineID != created.PipelineID {
		t.Errorf("FindPipeline = %+v", found)
	}
	all, errs := runclient.ListPipelinesAll([]string{f.url}, time.Hour, "", "")
	if len(errs) > 0 || len(all) != 1 {
		t.Errorf("ListPipelinesAll = %+v %v", all, errs)
	}

	// Resume of a fully completed run is rejected with the pinned 409 text.
	if _, err := runclient.ResumePipeline(f.url, created.PipelineID, "dk", "", nil); err == nil ||
		!strings.Contains(err.Error(), "nothing to resume") {
		t.Errorf("resume of finished pipeline = %v, want 'nothing to resume'", err)
	}
}
