package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/store"
)

// fakeLocal stands in for the container runtime: by default Run blocks
// until Stop closes the container's gate; a runFn override lets tests
// finish immediately or control the outcome.
type fakeLocal struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
	stops []string
	runFn func(job *api.JobStatus) executor.ExitInfo
}

func newFakeLocal() *fakeLocal {
	return &fakeLocal{gates: map[string]chan struct{}{}}
}

func (f *fakeLocal) Run(_ context.Context, job *api.JobStatus, _ executor.RunOptions) executor.ExitInfo {
	if f.runFn != nil {
		return f.runFn(job)
	}
	name := executor.ContainerName(job.Handler)
	ch := make(chan struct{})
	f.mu.Lock()
	f.gates[name] = ch
	f.mu.Unlock()
	<-ch
	return executor.ExitInfo{}
}

func (f *fakeLocal) Stop(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, name)
	if ch, ok := f.gates[name]; ok {
		close(ch)
		delete(f.gates, name)
	}
	return nil
}

func (f *fakeLocal) stopCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.stops...)
}

// fakeExternal stands in for a remote backend seam.
type fakeExternal struct {
	mu        sync.Mutex
	handle    string
	states    map[string]executor.HandleState
	submitted []*api.JobStatus
	cancels   []string
}

func (f *fakeExternal) Submit(_ context.Context, job *api.JobStatus) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, job)
	job.Queue = "extq"
	job.Region = "extr"
	return f.handle, nil
}

func (f *fakeExternal) Status(_ context.Context, handles []string) (map[string]executor.HandleState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]executor.HandleState{}
	for _, h := range handles {
		if s, ok := f.states[h]; ok {
			out[h] = s
		}
	}
	return out, nil
}

func (f *fakeExternal) Cancel(_ context.Context, handle, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, handle)
	return nil
}

func (f *fakeExternal) setState(handle string, s executor.HandleState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]executor.HandleState{}
	}
	f.states[handle] = s
}

func (f *fakeExternal) cancelCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.cancels...)
}

type testEnv struct {
	m        *Manager
	st       *store.Fake
	local    *fakeLocal
	ext      *fakeExternal
	finished chan *api.JobStatus
}

func newTestEnv(t *testing.T, specs map[string]executor.Spec, mutate func(*Config)) *testEnv {
	t.Helper()
	env := &testEnv{
		st:       store.NewFake(),
		local:    newFakeLocal(),
		ext:      &fakeExternal{handle: "h1"},
		finished: make(chan *api.JobStatus, 32),
	}
	cfg := Config{
		Local:      env.local,
		External:   env.ext,
		Store:      env.st,
		TotalCPU:   8,
		TotalRAMMB: 32768,
		ResolveSpec: func(jobType string) (executor.Spec, bool) {
			s, ok := specs[jobType]
			return s, ok
		},
		OnFinished:   func(j *api.JobStatus) { env.finished <- j },
		PollInterval: time.Hour, // tests drive pollOnce directly
	}
	if mutate != nil {
		mutate(&cfg)
	}
	env.m = New(cfg)
	t.Cleanup(env.m.Stop)
	return env
}

func (e *testEnv) submit(jobType, handler string) *Job {
	job := e.m.NewJob(api.JobRequest{JobType: jobType, Handler: handler, InputHash: "in"}, false, "dm")
	e.m.Submit(job)
	return job
}

func waitUntil(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func (e *testEnv) waitFinished(t *testing.T) *api.JobStatus {
	t.Helper()
	select {
	case j := <-e.finished:
		return j
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for OnFinished")
		return nil
	}
}

func TestLocalRunCompletes(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 1, RAMMB: 64}}
	env := newTestEnv(t, specs, nil)
	env.local.runFn = func(job *api.JobStatus) executor.ExitInfo {
		job.OutputHash = "out"
		return executor.ExitInfo{ExitCode: 0}
	}
	job := env.submit("CPUv1", "tool:v2")

	fin := env.waitFinished(t)
	if fin.JobID != job.JobID {
		t.Fatalf("finished %q, want %q", fin.JobID, job.JobID)
	}
	rec, ok := env.st.GetJob(job.JobID)
	if !ok {
		t.Fatal("job not in store")
	}
	if rec.Status != api.StatusDone || rec.ExitReason != "ok" || rec.OutputHash != "out" {
		t.Errorf("stored job: status=%q reason=%q out=%q", rec.Status, rec.ExitReason, rec.OutputHash)
	}
	if rec.Done != true || rec.IsRunning != false || rec.FinishedUnix == 0 || rec.RunningTime == "" {
		t.Errorf("terminal stamping wrong: %+v", rec)
	}
	// The resolved spec is recorded on the job (sizing is job-type driven).
	if rec.ReqCPU != 1 || rec.ReqRAMMB != 64 {
		t.Errorf("ReqCPU/ReqRAMMB = %d/%d, want 1/64", rec.ReqCPU, rec.ReqRAMMB)
	}
	// Resources and handler lock are released afterwards.
	waitUntil(t, "capacity released", func() bool { return env.m.CanFit(specs["CPUv1"]) })
	if s := env.m.Snapshot(); s.Running != 0 || s.QueueLength != 0 {
		t.Errorf("snapshot after completion: %+v", s)
	}
}

func TestFailedAndOOMExitReasons(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 1}}
	env := newTestEnv(t, specs, nil)
	env.local.runFn = func(job *api.JobStatus) executor.ExitInfo {
		job.ErrorMsg = "boom"
		return executor.ExitInfo{ExitCode: 137, OOMKilled: true}
	}
	job := env.submit("CPUv1", "tool:v2")
	env.waitFinished(t)
	rec, _ := env.st.GetJob(job.JobID)
	if rec.Status != api.StatusFailed || rec.ExitReason != "out-of-memory" {
		t.Errorf("status=%q reason=%q, want failed/out-of-memory", rec.Status, rec.ExitReason)
	}
}

func TestScanAndFitSkipsUnfitJobs(t *testing.T) {
	specs := map[string]executor.Spec{
		"big":   {CPU: 8},
		"small": {CPU: 2},
	}
	env := newTestEnv(t, specs, nil) // TotalCPU 8: big fits only alone
	big := env.submit("big", "bigtool")
	small := env.submit("small", "smalltool")

	// The big job is admitted first (it reserved the pool); the small job
	// must wait, so release big, then the scan admits small.
	waitUntil(t, "big running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if err := env.m.Cancel(big.JobID); err != nil {
		t.Fatal(err)
	}
	env.waitFinished(t)

	waitUntil(t, "small running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if got := env.m.RunningJobs()[0].JobID; got != small.JobID {
		t.Errorf("running = %q, want small %q", got, small.JobID)
	}
	if err := env.m.Cancel(small.JobID); err != nil {
		t.Fatal(err)
	}
	env.waitFinished(t)
}

func TestQueueOrderScanFit(t *testing.T) {
	// A job that cannot fit alongside the running one must not block an
	// unrelated smaller job behind it in the queue.
	specs := map[string]executor.Spec{
		"hold":  {CPU: 5},
		"unfit": {CPU: 6},
		"fit":   {CPU: 3},
	}
	env := newTestEnv(t, specs, nil) // TotalCPU 8
	hold := env.submit("hold", "holdh")
	unfit := env.submit("unfit", "unfith")
	fit := env.submit("fit", "fith")

	runningHas := func(id string) bool {
		for _, j := range env.m.RunningJobs() {
			if j.JobID == id {
				return true
			}
		}
		return false
	}
	waitUntil(t, "hold running", func() bool { return runningHas(hold.JobID) })
	// unfit (6) cannot join (5 used of 8); fit (3) can and must start.
	waitUntil(t, "fit admitted past unfit", func() bool { return runningHas(fit.JobID) })
	if runningHas(unfit.JobID) {
		t.Error("unfit job was admitted over capacity")
	}
	if env.m.Snapshot().QueueLength != 1 {
		t.Errorf("unfit job should still be queued: %+v", env.m.Snapshot())
	}
	env.m.Stop() // loops end; fake gate goroutines exit with the test
}

func TestHandlerSerialization(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 1}}
	env := newTestEnv(t, specs, nil)
	release := make(chan struct{})
	env.local.runFn = func(job *api.JobStatus) executor.ExitInfo {
		<-release
		return executor.ExitInfo{}
	}
	first := env.submit("CPUv1", "same:handler")
	second := env.submit("CPUv1", "same:handler")

	waitUntil(t, "first running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if s := env.m.Snapshot(); s.QueueLength != 1 {
		t.Fatalf("second job with same handler must queue: %+v", s)
	}
	close(release)
	env.waitFinished(t)
	waitUntil(t, "second running", func() bool {
		for _, j := range env.m.RunningJobs() {
			if j.JobID == second.JobID {
				return true
			}
		}
		return false
	})
	if first.JobID == second.JobID {
		t.Fatal("sanity")
	}
	env.m.Cancel(second.JobID)
}

func TestCancelQueuedJob(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 8}} // full pool: one at a time
	env := newTestEnv(t, specs, nil)
	running := env.submit("CPUv1", "first")
	queued := env.submit("CPUv1", "second")

	waitUntil(t, "first running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if err := env.m.Cancel(queued.JobID); err != nil {
		t.Fatal(err)
	}
	// A queued cancel never runs, so the finished hook fires directly.
	fin := env.waitFinished(t)
	if fin.JobID != queued.JobID {
		t.Fatalf("finished %q, want queued %q", fin.JobID, queued.JobID)
	}
	rec, _ := env.st.GetJob(queued.JobID)
	if rec.Status != api.StatusCancelled {
		t.Errorf("queued cancel status = %q", rec.Status)
	}
	if env.m.Snapshot().QueueLength != 0 {
		t.Error("queue should be empty after cancel")
	}
	env.m.Cancel(running.JobID)
}

func TestCancelRunningJob(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 1}}
	env := newTestEnv(t, specs, nil)
	job := env.submit("CPUv1", "tool:v2") // default fake blocks until Stop

	waitUntil(t, "job running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if err := env.m.Cancel(job.JobID); err != nil {
		t.Fatal(err)
	}
	if got := env.local.stopCalls(); len(got) != 1 || got[0] != "tool-v2" {
		t.Errorf("stop calls = %q, want [tool-v2]", got)
	}
	env.waitFinished(t)
	rec, _ := env.st.GetJob(job.JobID)
	if rec.Status != api.StatusCancelled || rec.ExitReason != "cancelled" {
		t.Errorf("status=%q reason=%q, want cancelled/cancelled", rec.Status, rec.ExitReason)
	}
}

func TestCancelUnknown(t *testing.T) {
	env := newTestEnv(t, nil, nil)
	if err := env.m.Cancel("nope"); err == nil {
		t.Error("cancel of unknown job must error")
	}
}

func TestCapacityManagement(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 4, RAMMB: 1000, GPU: 1}}
	env := newTestEnv(t, specs, func(c *Config) { c.TotalGPU = 1 })
	if !env.m.WithinCapacity(specs["CPUv1"]) {
		t.Error("spec should fit the totals")
	}
	if env.m.WithinCapacity(executor.Spec{CPU: 9}) {
		t.Error("spec above totals must never fit")
	}
	job := env.submit("CPUv1", "tool")
	waitUntil(t, "gpu job running", func() bool { return len(env.m.RunningJobs()) == 1 })
	if env.m.SetCapacity(2, 100, 0) {
		t.Error("must refuse capacity below the reserved pool")
	}
	if s := env.m.Snapshot(); s.TotalCPU != 8 || s.TotalGPU != 1 {
		t.Errorf("refused SetCapacity changed totals: %+v", s)
	}
	env.m.Cancel(job.JobID)
	env.waitFinished(t)
	waitUntil(t, "capacity released", func() bool { return env.m.SetCapacity(2, 100, 0) })
}

func TestExternalLifecycle(t *testing.T) {
	env := newTestEnv(t, nil, nil) // no local spec for ext types
	job := env.m.NewJob(api.JobRequest{JobType: "ext.big", Handler: "t", InputHash: "in"}, false, "dm")
	if err := env.m.SubmitExternal(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	rec, _ := env.st.GetJob(job.JobID)
	if rec.BatchJobID != "h1" || rec.Queue != "extq" || rec.Region != "extr" {
		t.Errorf("external provenance not persisted: %q %q %q", rec.BatchJobID, rec.Queue, rec.Region)
	}

	env.ext.setState("h1", executor.HandleState{Status: api.StatusRunning})
	env.m.pollOnce()
	waitUntil(t, "running", func() bool {
		rec, _ := env.st.GetJob(job.JobID)
		return rec.Status == api.StatusRunning && rec.StartedUnix > 0
	})

	env.ext.setState("h1", executor.HandleState{Status: api.StatusFailed, ExitCode: 7, Reason: "backend said no"})
	env.m.pollOnce()
	fin := env.waitFinished(t)
	rec, _ = env.st.GetJob(job.JobID)
	if rec.Status != api.StatusFailed || rec.ExitReason != "error" {
		t.Errorf("status=%q reason=%q", rec.Status, rec.ExitReason)
	}
	if rec.ErrorMsg != "backend said no" {
		t.Errorf("ErrorMsg = %q", rec.ErrorMsg)
	}
	_ = fin
}

func TestExternalCancelAndPoll(t *testing.T) {
	env := newTestEnv(t, nil, nil)
	job := env.m.NewJob(api.JobRequest{JobType: "ext.big", Handler: "t", InputHash: "in"}, false, "dm")
	if err := env.m.SubmitExternal(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := env.m.Cancel(job.JobID); err != nil {
		t.Fatal(err)
	}
	if got := env.ext.cancelCalls(); len(got) != 1 || got[0] != "h1" {
		t.Errorf("backend cancels = %q", got)
	}
	// Cancel alone does not finalize; the poller does.
	select {
	case <-env.finished:
		t.Fatal("external cancel must not finalize immediately")
	case <-time.After(50 * time.Millisecond):
	}
	env.ext.setState("h1", executor.HandleState{Status: api.StatusCancelled})
	env.m.pollOnce()
	// A cancelled backend state is non-terminal to the poller (it maps to
	// status updates), but the job carries cancelled=true so finalize on
	// a terminal state keeps StatusCancelled. Push it to failed:
	env.ext.setState("h1", executor.HandleState{Status: api.StatusFailed, Reason: "terminated"})
	env.m.pollOnce()
	env.waitFinished(t)
	rec, _ := env.st.GetJob(job.JobID)
	if rec.Status != api.StatusCancelled {
		t.Errorf("status = %q, want cancelled (user cancel wins)", rec.Status)
	}
}

func TestExternalTimeout(t *testing.T) {
	env := newTestEnv(t, nil, func(c *Config) { c.ExternalTimeout = 10 * time.Millisecond })
	job := env.m.NewJob(api.JobRequest{JobType: "ext.big", Handler: "t", InputHash: "in"}, false, "dm")
	if err := env.m.SubmitExternal(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	env.m.pollOnce()
	env.waitFinished(t)
	if got := env.ext.cancelCalls(); len(got) != 1 {
		t.Errorf("timed-out job must be cancelled at the backend: %q", got)
	}
	rec, _ := env.st.GetJob(job.JobID)
	if rec.Status != api.StatusFailed || rec.ExitReason != "error" {
		t.Errorf("timeout outcome: %q/%q", rec.Status, rec.ExitReason)
	}
}

func TestRestoreExternalAndPoll(t *testing.T) {
	// A job from a previous run: non-terminal with an ext-ref in the store.
	env := newTestEnv(t, nil, nil)
	env.st.UpsertJob(&api.JobStatus{
		JobID: "old1", JobType: "ext.big", Handler: "t", InputHash: "in",
		Status: api.StatusQueued, BatchJobID: "h1", CreatedUnix: time.Now().Unix(),
	})
	n, err := env.m.RestoreExternal()
	if err != nil || n != 1 {
		t.Fatalf("restore = %d, %v", n, err)
	}
	if _, ok := env.m.Live("old1"); !ok {
		t.Fatal("restored job not live")
	}
	env.ext.setState("h1", executor.HandleState{Status: api.StatusDone, ExitCode: 0})
	env.m.pollOnce()
	env.waitFinished(t)
	rec, _ := env.st.GetJob("old1")
	if rec.Status != api.StatusDone || rec.ExitReason != "ok" {
		t.Errorf("resumed job finalize: %q/%q", rec.Status, rec.ExitReason)
	}
}

func TestSubmitExternalWithoutBackend(t *testing.T) {
	env := newTestEnv(t, nil, func(c *Config) { c.External = nil })
	job := env.m.NewJob(api.JobRequest{JobType: "ext.big", Handler: "t", InputHash: "in"}, false, "dm")
	if err := env.m.SubmitExternal(context.Background(), job); err == nil {
		t.Error("expected ErrNoExternalBackend")
	}
}

func TestCleanUpRetainsThenDrops(t *testing.T) {
	specs := map[string]executor.Spec{"CPUv1": {CPU: 1}}
	env := newTestEnv(t, specs, nil)
	env.local.runFn = func(job *api.JobStatus) executor.ExitInfo { return executor.ExitInfo{} }
	job := env.submit("CPUv1", "tool")
	env.waitFinished(t)

	// Retention is a week; a freshly finished job stays live.
	env.m.CleanUp()
	if _, ok := env.m.Live(job.JobID); !ok {
		t.Fatal("fresh job must survive cleanup")
	}
	// Backdate past retention: now it goes.
	live, _ := env.m.Live(job.JobID)
	live.deletionAt = time.Now().Add(-time.Hour)
	env.m.CleanUp()
	if _, ok := env.m.Live(job.JobID); ok {
		t.Fatal("expired job must be cleaned up")
	}
}
