package pipeline

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/store"
)

// Integration tests drive the whole advance loop against the in-memory
// store and an in-memory filehost: real manifest fetches, real composed
// uploads, real job records — the only fake is the job submission
// itself, which stands in for the scheduler seam.

// fakeFilehost serves GET /{hash} from the blob map and PUT stores the
// body under its computed content address, echoing the address back —
// the shape compose.Build's upload expects.
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

// hashOf renders a deterministic 64-char hex content address from a
// readable label (shared technique with the compose tests).
func hashOf(label string) string {
	h := strings.Repeat("0", 64-len(label)) + label
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("0123456789abcdef", r) {
			return r
		}
		return 'a'
	}, h)
}

// dirManifest renders a tiedir-v2 blob from entries; fileEntry a plain
// file entry (same fixtures as the compose tests).
func dirManifest(entries ...metadata.DirEntry) string {
	var b strings.Builder
	b.WriteString(metadata.DirHeader)
	for _, e := range entries {
		b.WriteString(e.Line())
	}
	return b.String()
}

func fileEntry(name, hash string) metadata.DirEntry {
	return metadata.DirEntry{Hash: hash, Filename: name, Size: 3, Head: []byte("abc")}
}

// shortHash abbreviates a content address for failure messages.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// submitRecorder stands in for the scheduler seam: every submitted step
// becomes a stored queued job.
type submitRecorder struct {
	mu   sync.Mutex
	reqs []api.JobRequest
	seq  int
	st   store.Store
	err  error
}

func (s *submitRecorder) submit(req api.JobRequest, deployManager string) (*api.JobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.seq++
	s.reqs = append(s.reqs, req)
	job := &api.JobStatus{
		JobID:         fmt.Sprintf("job-%d", s.seq),
		DeployManager: deployManager,
		JobType:       req.JobType,
		Handler:       req.Handler,
		InputHash:     req.InputHash,
		AppHash:       req.AppHash,
		User:          req.User,
		PipelineID:    req.PipelineID,
		StepName:      req.StepName,
		Status:        api.StatusQueued,
		CreatedUnix:   time.Now().Unix(),
	}
	s.st.UpsertJob(job)
	return job, nil
}

func (s *submitRecorder) requests() []api.JobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.JobRequest{}, s.reqs...)
}

func (s *submitRecorder) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// finishJob applies a terminal outcome to a recorded job, as the
// scheduler's finalize would.
func finishJob(st store.Store, jobID, status, outputHash, errMsg string) *api.JobStatus {
	job, ok := st.GetJob(jobID)
	if !ok {
		panic("no such job " + jobID)
	}
	job.Status = status
	job.OutputHash = outputHash
	job.ErrorMsg = errMsg
	job.FinishedUnix = time.Now().Unix()
	st.UpsertJob(job)
	return job
}

// stepByID finds a step in a freshly loaded pipeline.
func stepByID(t *testing.T, st store.Store, pipelineID, name string) *api.PipelineStep {
	t.Helper()
	p, ok := st.GetPipeline(pipelineID)
	if !ok {
		t.Fatalf("pipeline %s not found", pipelineID)
	}
	s := step(p, name)
	if s == nil {
		t.Fatalf("step %q not in pipeline", name)
	}
	return s
}

func waitPipeline(t *testing.T, desc string, cond func() bool) {
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

type harness struct {
	st  *store.Fake
	rec *submitRecorder
	eng *Engine
	url string
}

func newHarness(t *testing.T, blobs map[string]string) *harness {
	t.Helper()
	h := &harness{st: store.NewFake()}
	h.url = fakeFilehost(t, blobs)
	h.rec = &submitRecorder{st: h.st}
	h.eng = New(Config{
		Store:        h.st,
		SubmitJob:    h.rec.submit,
		CancelJob:    func(string) error { return nil },
		ValidateStep: knownTypes,
		Filehost:     h.url,
		StoreName:    "archive",
	})
	return h
}

func twoStepRequest() *api.PipelineRequest {
	return &api.PipelineRequest{
		Name: "chain",
		User: "tester",
		Steps: []api.PipelineStep{
			stepOf("prep", jobType),
			stepOf("cluster", jobType, from("prep", "out.csv")),
		},
	}
}

// The full happy path: two chained steps, each step's input composed
// from its predecessor's recorded output, until the pipeline closes done.
func TestAdvanceEndToEnd(t *testing.T) {
	leaf := hashOf("leaf")
	prepOut := hashOf("prepout")
	blobs := map[string]string{
		prepOut: dirManifest(fileEntry("out.csv", leaf)),
		leaf:    "abc",
	}
	h := newHarness(t, blobs)

	p, err := h.eng.Submit(twoStepRequest(), "dm")
	if err != nil {
		t.Fatal(err)
	}
	pid := p.PipelineID

	// prep is running immediately; its input is the composed empty tree.
	if s := stepByID(t, h.st, pid, "prep"); s.Status != api.StepRunning || s.JobID == "" {
		t.Fatalf("prep should be running: %+v", s)
	}
	if got := stepByID(t, h.st, pid, "cluster"); got.Status != api.StepPending {
		t.Fatalf("cluster must wait: %+v", got)
	}
	if reqs := h.rec.requests(); len(reqs) != 1 || len(reqs[0].InputHash) != 64 {
		t.Fatalf("prep submission wrong: %+v", reqs)
	}

	// finish prep with a real tree; advancing composes cluster's input
	// from it and submits cluster.
	job := stepByID(t, h.st, pid, "prep").JobID
	h.eng.JobFinished(finishJob(h.st, job, api.StatusDone, prepOut, ""))

	waitPipeline(t, "cluster submitted", func() bool {
		return stepByID(t, h.st, pid, "cluster").Status == api.StepRunning
	})
	reqs := h.rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("submissions = %d, want 2", len(reqs))
	}
	// The composed input must resolve on the filehost and carry the
	// selected entry — this is the whole zero-data-movement contract.
	items, err := compose.DirTree(h.url, reqs[1].InputHash, "")
	if err != nil {
		t.Fatalf("cluster input %s does not resolve: %v", reqs[1].InputHash, err)
	}
	if len(items) != 1 || len(items[0].Children) != 1 {
		t.Fatalf("composed input tree wrong: %+v", items)
	}
	if c := items[0].Children[0]; c.Name != "out.csv" || c.Hash != leaf {
		t.Errorf("composed entry = %+v, want out.csv@%s", c, shortHash(leaf))
	}

	job = stepByID(t, h.st, pid, "cluster").JobID
	h.eng.JobFinished(finishJob(h.st, job, api.StatusDone, hashOf("final"), ""))

	waitPipeline(t, "pipeline done", func() bool {
		p, ok := h.st.GetPipeline(pid)
		return ok && p.Status == api.StatusDone
	})
}

// A failing job fails its step; required dependents get skipped with a
// reason; the pipeline is failed, never reported as success.
func TestFailurePropagates(t *testing.T) {
	h := newHarness(t, map[string]string{})
	p, err := h.eng.Submit(twoStepRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	pid := p.PipelineID

	job := stepByID(t, h.st, pid, "prep").JobID
	h.eng.JobFinished(finishJob(h.st, job, api.StatusFailed, "", "boom"))

	waitPipeline(t, "pipeline failed", func() bool {
		p, ok := h.st.GetPipeline(pid)
		return ok && p.Status == api.StatusFailed
	})
	if s := stepByID(t, h.st, pid, "prep"); s.Status != api.StepFailed || s.ErrorMsg != "boom" {
		t.Errorf("prep step state: %+v", s)
	}
	s := stepByID(t, h.st, pid, "cluster")
	if s.Status != api.StepSkipped || !strings.Contains(s.ErrorMsg, `required input from step "prep" failed`) {
		t.Errorf("cluster step state: %+v", s)
	}
	p, _ = h.st.GetPipeline(pid)
	if !strings.Contains(p.ErrorMsg, "steps did not complete: prep, cluster") {
		t.Errorf("pipeline ErrorMsg = %q", p.ErrorMsg)
	}
}

// A submission error fails the step in place (no retry loop).
func TestSubmissionFailureFailsStep(t *testing.T) {
	h := newHarness(t, map[string]string{})
	h.rec.setErr(fmt.Errorf("filehost unreachable"))
	p, err := h.eng.Submit(twoStepRequest(), "")
	if err != nil {
		t.Fatal(err) // submission itself succeeds; the step fails
	}
	waitPipeline(t, "pipeline failed", func() bool {
		pp, ok := h.st.GetPipeline(p.PipelineID)
		return ok && pp.Status == api.StatusFailed
	})
	if s := stepByID(t, h.st, p.PipelineID, "prep"); s.Status != api.StepFailed || !strings.Contains(s.ErrorMsg, "filehost unreachable") {
		t.Errorf("prep step state: %+v", s)
	}
}

// Cancel stops the running step's job, skips pending ones, and later job
// outcomes must not advance a cancelled pipeline. Resume then re-runs the
// unfinished tail.
func TestCancelPipeline(t *testing.T) {
	leaf := hashOf("leaf")
	prepOut := hashOf("prepout")
	blobs := map[string]string{prepOut: dirManifest(fileEntry("out.csv", leaf)), leaf: "abc"}
	h := newHarness(t, blobs)

	var cancelled []string
	var cmu sync.Mutex
	h.eng.cfg.CancelJob = func(jobID string) error {
		cmu.Lock()
		cancelled = append(cancelled, jobID)
		cmu.Unlock()
		return nil
	}

	p, err := h.eng.Submit(twoStepRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	pid := p.PipelineID

	if err := h.eng.Cancel(pid); err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 1 {
		t.Errorf("running step job must be cancelled: %v", cancelled)
	}
	if s := stepByID(t, h.st, pid, "prep"); s.Status != api.StepFailed || s.ErrorMsg != "cancelled by user" {
		t.Errorf("running step after cancel: %+v", s)
	}
	if s := stepByID(t, h.st, pid, "cluster"); s.Status != api.StepSkipped {
		t.Errorf("pending step after cancel: %+v", s)
	}
	p, _ = h.st.GetPipeline(pid)
	if p.Status != api.StatusCancelled {
		t.Errorf("pipeline status = %q", p.Status)
	}

	// The cancelled job finishing must not advance the cancelled run.
	h.eng.JobFinished(finishJob(h.st, "job-1", api.StatusDone, prepOut, ""))
	time.Sleep(50 * time.Millisecond)
	if reqs := h.rec.requests(); len(reqs) != 1 {
		t.Errorf("cancelled pipeline submitted %d jobs, want 1", len(reqs))
	}

	// Resume retries the unfinished tail (prep reruns; cluster follows).
	if _, err := h.eng.Resume(pid, "", nil); err != nil {
		t.Fatal(err)
	}
	waitPipeline(t, "prep running again", func() bool {
		return stepByID(t, h.st, pid, "prep").Status == api.StepRunning
	})
	job := stepByID(t, h.st, pid, "prep").JobID
	h.eng.JobFinished(finishJob(h.st, job, api.StatusDone, prepOut, ""))
	waitPipeline(t, "cluster running", func() bool {
		return stepByID(t, h.st, pid, "cluster").Status == api.StepRunning
	})
	job = stepByID(t, h.st, pid, "cluster").JobID
	h.eng.JobFinished(finishJob(h.st, job, api.StatusDone, hashOf("final"), ""))
	waitPipeline(t, "pipeline done", func() bool {
		p, ok := h.st.GetPipeline(pid)
		return ok && p.Status == api.StatusDone
	})
}

// Resume with an override supplies a fixed step output instead of
// rerunning it; downstream steps compose from the supplied hash.
func TestResumeWithOverrides(t *testing.T) {
	altLeaf, goodOut := hashOf("altleaf"), hashOf("goodout")
	blobs := map[string]string{
		goodOut: dirManifest(fileEntry("out.csv", altLeaf)),
		altLeaf: "xyz",
	}
	h := newHarness(t, blobs)

	p, err := h.eng.Submit(twoStepRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	pid := p.PipelineID
	// prep produced a tree that does not resolve on the filehost: the
	// dependent's composition fails and the step fails at submission.
	h.eng.JobFinished(finishJob(h.st, "job-1", api.StatusDone, hashOf("unresolvable"), ""))
	waitPipeline(t, "pipeline failed", func() bool {
		pp, ok := h.st.GetPipeline(pid)
		return ok && pp.Status == api.StatusFailed
	})
	if s := stepByID(t, h.st, pid, "cluster"); s.Status != api.StepFailed {
		t.Fatalf("cluster should have failed composing: %+v", s)
	}

	// Repair by hand: override prep's output with a fixed tree.
	if _, err := h.eng.Resume(pid, "", map[string]string{"prep": goodOut}); err != nil {
		t.Fatal(err)
	}
	if s := stepByID(t, h.st, pid, "prep"); s.Status != api.StepDone || s.OutputHash != goodOut {
		t.Fatalf("override not applied: %+v", s)
	}
	waitPipeline(t, "cluster submitted from override", func() bool {
		return stepByID(t, h.st, pid, "cluster").Status == api.StepRunning
	})
	reqs := h.rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("prep must not rerun under override: %d submissions", len(reqs))
	}
	items, err := compose.DirTree(h.url, reqs[1].InputHash, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Children) != 1 || items[0].Children[0].Hash != altLeaf {
		t.Errorf("cluster input not composed from the override: %+v", items)
	}
}

func TestResumeRejects(t *testing.T) {
	h := newHarness(t, map[string]string{})
	if _, err := h.eng.Resume("nope", "", nil); err == nil {
		t.Error("unknown pipeline must error")
	}

	p, err := h.eng.Submit(twoStepRequest(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Resume(p.PipelineID, "", nil); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("running pipeline resume: %v", err)
	}

	// A finished run exercises the step-name and override checks.
	h.st.UpdatePipelineStatus(p.PipelineID, api.StatusDone, "", time.Now().Unix())
	if _, err := h.eng.Resume(p.PipelineID, "nosuchstep", nil); err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("unknown from-step: %v", err)
	}
	if _, err := h.eng.Resume(p.PipelineID, "", map[string]string{"nosuchstep": seedHash}); err == nil || !strings.Contains(err.Error(), "override names unknown step") {
		t.Errorf("bad override name: %v", err)
	}
	// Everything already completed (prep ran; cluster was reset to done
	// by an override-shaped state): nothing left to retry.
	for _, s := range []string{"prep", "cluster"} {
		st := stepByID(t, h.st, p.PipelineID, s)
		st.Status = api.StepDone
		st.OutputHash = seedHash
		h.st.UpsertPipelineStep(p.PipelineID, st)
	}
	if _, err := h.eng.Resume(p.PipelineID, "", nil); err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Errorf("fully completed resume: %v", err)
	}
}

// A restart resumes interrupted runs: pending steps are simply advanced
// again because all state lives in the store.
func TestResumeAllInterrupted(t *testing.T) {
	h := newHarness(t, map[string]string{})
	p := &api.Pipeline{
		PipelineID: "restarted",
		Status:     api.StatusRunning,
		Steps: []*api.PipelineStep{
			{Name: "prep", Ordinal: 0, Status: api.StepPending, Handler: "prep:latest", JobType: jobType},
		},
	}
	if err := h.st.InsertPipeline(p); err != nil {
		t.Fatal(err)
	}
	h.eng.ResumeAll()
	waitPipeline(t, "interrupted step resubmitted", func() bool {
		return stepByID(t, h.st, "restarted", "prep").Status == api.StepRunning
	})
}
