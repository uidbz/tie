package runclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/runner/api"
)

// testTreeHash is a stand-in tree address; the fake runner never resolves it.
const testTreeHash = "0000000000000000000000000000000000000000000000000000000000000abc"

// testPipeline returns a small pipeline for fan-out tests.
func testPipeline(id, name string, created int64) api.Pipeline {
	return api.Pipeline{
		PipelineID:  id,
		Name:        name,
		User:        "tester",
		Status:      "running",
		CreatedUnix: created,
		Steps: []*api.PipelineStep{
			{Name: "prep", Handler: "tool:v2", JobType: "CPUv1", Ordinal: 0, Status: "running"},
		},
	}
}

// TestSubmitPipelineRequestShape pins POST /api/pipelines: the whole
// graph is posted in one request, User is defaulted, and the step's
// Inputs carry FromStep names rather than client-resolved hashes (the
// runner resolves them — resolving client-side is the design bug the
// pipeline feature exists to avoid).
func TestSubmitPipelineRequestShape(t *testing.T) {
	var gotPath, gotMethod string
	var got api.PipelineRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: "running"})
	}))
	defer srv.Close()

	_, err := SubmitPipeline(srv.URL, api.PipelineRequest{
		Name:  "discovery",
		Seeds: map[string]string{"raw": testTreeHash},
		Steps: []api.PipelineStep{{
			Name:     "prep",
			Handler:  "tool:v2",
			JobType:  "CPUv1",
			Settings: json.RawMessage(`{"tolerance":0.005}`),
			Inputs:   []compose.Spec{{FromStep: "raw"}},
		}},
	})
	if err != nil {
		t.Fatalf("SubmitPipeline: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/pipelines" {
		t.Errorf("%s %s, want POST /api/pipelines", gotMethod, gotPath)
	}
	if got.Name != "discovery" || got.Seeds["raw"] != testTreeHash {
		t.Errorf("name/seeds wrong: %+v", got)
	}
	if got.User == "" {
		t.Error("User should be defaulted for attribution")
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(got.Steps))
	}
	s := got.Steps[0]
	if s.Name != "prep" || s.Handler != "tool:v2" || s.JobType != "CPUv1" {
		t.Errorf("step wrong: %+v", s)
	}
	if string(s.Settings) != `{"tolerance":0.005}` {
		t.Errorf("Settings not passed through raw: %s", s.Settings)
	}
	if len(s.Inputs) != 1 || s.Inputs[0].FromStep != "raw" || s.Inputs[0].Hash != "" {
		t.Errorf("input should name step \"raw\" with no hash: %+v", s.Inputs)
	}
}

// --- pipeline helpers over the frozen api.Pipeline DTO ---------------------

func TestEntryHandlerSkipsSeeds(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "seed", Ordinal: 0},                    // a seed: no handler
		{Name: "second", Handler: "b:v1", Ordinal: 1}, //
		{Name: "first", Handler: "a:v1", Ordinal: 2},
	}}
	if got := EntryHandler(p); got != "b:v1" {
		t.Errorf("EntryHandler = %q, want b:v1 (lowest ordinal that runs)", got)
	}
	if Step(p, "nope") != nil {
		t.Error("Step of unknown name must be nil")
	}
	if Step(p, "first").Handler != "a:v1" {
		t.Error("Step lookup broken")
	}
	if PipelineDone(p) {
		t.Error("running pipeline reported done")
	}
	p.Status = api.StatusCancelled
	if !PipelineDone(p) {
		t.Error("cancelled pipeline must report done")
	}
}

// --- polling / wait semantics ----------------------------------------------

// fakePipelineRunner serves /api/pipelines/<id>: first `failFirst`
// requests answer 500, then the pipeline reports done.
func fakePipelineRunner(t *testing.T, failFirst int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(hits.Add(1)) <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: "done"})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestWaitPipelineSurvivesTransientErrors is the pipeline mirror of the
// job-watch tolerance test.
func TestWaitPipelineSurvivesTransientErrors(t *testing.T) {
	withFastBackoff(t)
	srv, hits := fakePipelineRunner(t, 3)

	p, err := WaitPipelineCtx(context.Background(), srv.URL, "p1", time.Millisecond, nil)
	if err != nil {
		t.Fatalf("WaitPipelineCtx aborted on transient errors: %v", err)
	}
	if p.Status != "done" {
		t.Errorf("pipeline status = %q, want done", p.Status)
	}
	if got := hits.Load(); got != 4 {
		t.Errorf("pipeline requests = %d, want 4 (3 failures + 1 success)", got)
	}
}

// TestWaitPipelineGivesUpAfterBudget pins the bounded tolerance for
// pipelines: the error names the resume command.
func TestWaitPipelineGivesUpAfterBudget(t *testing.T) {
	withFastBackoff(t)
	srv, _ := fakePipelineRunner(t, 1000)

	_, err := WaitPipelineCtx(context.Background(), srv.URL, "p1", time.Millisecond, nil)
	if err == nil {
		t.Fatal("WaitPipelineCtx returned nil after exceeding the failure budget")
	}
	if want := "tie-run pipeline show p1"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should name the resume command %q", err, want)
	}
}

// --- fleet merge over pipelines --------------------------------------------

func TestListPipelinesAllMergesSortsAndTagsServers(t *testing.T) {
	mk := func(ps ...api.Pipeline) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/pipelines" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			if got := r.URL.Query().Get("status"); got != "running" {
				t.Errorf("status filter lost: %q", got)
			}
			if err := json.NewEncoder(w).Encode(ps); err != nil {
				t.Error(err)
			}
		}))
	}
	a := mk(testPipeline("p1", "one", 100))
	defer a.Close()
	b := mk(testPipeline("p2", "two", 300), testPipeline("p3", "three", 200))
	defer b.Close()

	got, errs := ListPipelinesAll([]string{a.URL, b.URL}, 720*time.Hour, "tester", "running")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 pipelines, got %d", len(got))
	}
	for i, want := range []string{"p2", "p3", "p1"} {
		if got[i].Value.PipelineID != want {
			t.Errorf("position %d: got %s, want %s", i, got[i].Value.PipelineID, want)
		}
	}
	if got[0].Server != b.URL || got[2].Server != a.URL {
		t.Errorf("server tags lost: %+v", got)
	}
}

func TestListPipelinesAllPerServerErrors(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode([]api.Pipeline{testPipeline("p1", "one", 100)}); err != nil {
			t.Error(err)
		}
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer b.Close()

	got, errs := ListPipelinesAll([]string{a.URL, b.URL}, 0, "", "")
	if len(errs) != 1 {
		t.Fatalf("expected 1 server error, got %v", errs)
	}
	if _, ok := errs[b.URL]; !ok {
		t.Errorf("error not tagged to the failing server: %v", errs)
	}
	if len(got) != 1 || got[0].Value.PipelineID != "p1" {
		t.Fatalf("expected the healthy server's pipeline, got %+v", got)
	}
}

func TestFindPipelineSkips404s(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no pipeline with ID p1", http.StatusNotFound)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pipelines/p1" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(testPipeline("p1", "one", 100)); err != nil {
			t.Error(err)
		}
	}))
	defer b.Close()

	got, err := FindPipeline([]string{a.URL, b.URL}, "p1")
	if err != nil {
		t.Fatalf("FindPipeline: %v", err)
	}
	if got.Value.PipelineID != "p1" || got.Server != b.URL {
		t.Errorf("wrong pipeline or server tag: %+v", got)
	}
}

func TestFindPipelineNotFound(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no pipeline with ID p1", http.StatusNotFound)
	}))
	defer a.Close()

	if _, err := FindPipeline([]string{a.URL}, "p1"); err == nil {
		t.Error("expected not-found error when every server 404s")
	}
}

func TestFindPipelineUnreachable(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()

	if _, err := FindPipeline([]string{bad.URL}, "p1"); err == nil {
		t.Error("expected an error for an unreachable runner")
	}
}
