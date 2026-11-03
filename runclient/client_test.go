package runclient

// Wire-contract test for POST /api/run plus the progress-watch backoff
// semantics. The runner's own tests check the other side; these pin the
// client so a field rename or a dropped default cannot drift silently
// into a job that runs with the wrong attribution or no settings.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// TestRunRequestShape pins POST /api/run: User is defaulted to the
// current user when the caller leaves it empty, and Settings is passed
// through raw (json.RawMessage must not be re-encoded or dropped — the
// runner parses it itself).
func TestRunRequestShape(t *testing.T) {
	var gotPath, gotMethod string
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1"})
	}))
	defer srv.Close()

	// Trailing slash on purpose: normalizeTarget must absorb it.
	if _, err := Run(srv.URL+"/", api.JobRequest{
		JobType:   "CPUv1",
		Handler:   "add5:latest",
		InputHash: "abc",
		// Raw JSON with odd spacing: byte-level passthrough is the contract.
		Settings: json.RawMessage(`{ "A":  "setting" }`),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/run" {
		t.Errorf("%s %s, want POST /api/run (no double slash)", gotMethod, gotPath)
	}

	if len(body["User"]) == 0 || string(body["User"]) == `""` {
		t.Errorf("User not defaulted: body = %v", body)
	}
	if string(body["User"]) != `"`+CurrentUser()+`"` {
		t.Errorf("User = %s, want the current user %q", body["User"], CurrentUser())
	}
	if len(body["Settings"]) == 0 {
		t.Fatalf("Settings missing from body: %v", body)
	}
	// CURRENT-BEHAVIOR PIN: encoding/json compacts a json.RawMessage when
	// it marshals the request struct, so the JSON VALUE survives but the
	// byte spacing does not. What must not happen is re-encoding through
	// a map (key order / number format drift) or dropping the field.
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(`{ "A":  "setting" }`)); err != nil {
		t.Fatal(err)
	}
	if string(body["Settings"]) != want.String() {
		t.Errorf("Settings = %s, want the compacted source JSON %s", body["Settings"], want.String())
	}
	if string(body["JobType"]) != `"CPUv1"` || string(body["Handler"]) != `"add5:latest"` ||
		string(body["InputHash"]) != `"abc"` {
		t.Errorf("core fields wrong: %v", body)
	}
}

// Transient-error tolerance and cancellation for the polling loops: a
// runner restart or Wi-Fi blip must not kill the watch. The backoff
// envelope is shrunk to milliseconds so the tolerance tests do not sleep
// for minutes; the production defaults (10 failures, 1s -> 30s) are
// asserted separately.

func withFastBackoff(t *testing.T) {
	t.Helper()
	start, max, n := pollBackoffStart, pollBackoffMax, pollMaxConsecutiveErrors
	t.Cleanup(func() {
		pollBackoffStart, pollBackoffMax, pollMaxConsecutiveErrors = start, max, n
	})
	pollBackoffStart = time.Millisecond
	pollBackoffMax = 5 * time.Millisecond
}

// fakeStatusRunner serves /api/status/<id>. The first `failFirst` status
// requests answer 500 (a transient runner blip); after that the job is
// done.
func fakeStatusRunner(t *testing.T, failFirst int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/status/") {
			http.NotFound(w, r)
			return
		}
		if int(hits.Add(1)) <= failFirst {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "job1", Done: true, Percent: 100, RunningTime: "1s"})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestShowProgressSurvivesTransientErrors pins that a handful of failed
// status fetches mid-watch no longer abort the wait: the job keeps
// running on the runner and the watch rides out the blip.
func TestShowProgressSurvivesTransientErrors(t *testing.T) {
	withFastBackoff(t)
	srv, hits := fakeStatusRunner(t, 3)

	status, err := ShowProgressCtx(context.Background(), srv.URL, "job1", true, true)
	if err != nil {
		t.Fatalf("ShowProgressCtx aborted on transient errors: %v", err)
	}
	if !status.Done {
		t.Errorf("status.Done = false, want true after recovery")
	}
	if got := hits.Load(); got != 4 {
		t.Errorf("status requests = %d, want 4 (3 failures + 1 success)", got)
	}
}

// TestShowProgressResetsBackoffOnSuccess pins the reset: 9 failures, a
// success, 9 more failures, a success must survive, even though 18 > 10
// failures happened in total — only *consecutive* failures count.
func TestShowProgressResetsBackoffOnSuccess(t *testing.T) {
	withFastBackoff(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		switch {
		case n <= 9 || (n >= 11 && n <= 19):
			w.WriteHeader(http.StatusBadGateway)
		default:
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "job1", Done: true})
		}
	}))
	defer srv.Close()

	if _, err := ShowProgressCtx(context.Background(), srv.URL, "job1", true, true); err != nil {
		t.Fatalf("ShowProgressCtx gave up despite a success between failure runs: %v", err)
	}
}

// TestShowProgressGivesUpAfterBudget pins that the tolerance is bounded:
// 10 consecutive failures end the wait with an error that names the
// runner and the resume command, instead of polling forever or dying
// silently.
func TestShowProgressGivesUpAfterBudget(t *testing.T) {
	withFastBackoff(t)
	srv, hits := fakeStatusRunner(t, 1000)

	_, err := ShowProgressCtx(context.Background(), srv.URL, "job1", true, true)
	if err == nil {
		t.Fatal("ShowProgressCtx returned nil after exceeding the failure budget")
	}
	if !strings.Contains(err.Error(), "tie-run status job1") {
		t.Errorf("error = %q, want it to mention 'tie-run status job1'", err)
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error = %q, want it to name the runner %s", err, srv.URL)
	}
	if got := hits.Load(); got != 10 {
		t.Errorf("status requests = %d, want 10 (budget)", got)
	}
}

// TestShowProgressContextCancel pins that a cancelled context ends the
// watch promptly with a benign nil error — the job is unaffected.
// TestWatchHintsNameTheCLI pins the CLIName seam: the watch-cancel advice
// must name the shell's own binary, not the engine default. A branded
// shell sets CLIName once (like AuthUser) and every hint follows.
func TestWatchHintsNameTheCLI(t *testing.T) {
	old := CLIName
	t.Cleanup(func() { CLIName = old })
	CLIName = "mytool"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "job1", IsRunning: true, Percent: 50})
	}))
	defer srv.Close()

	out := captureStdout(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ShowProgressCtx(ctx, srv.URL, "job1", true, true); err != nil {
			t.Errorf("cancelled watch: %v", err)
		}
		psrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: "running"})
		}))
		defer psrv.Close()
		ctx2, cancel2 := context.WithCancel(context.Background())
		cancel2()
		if _, err := WaitPipelineCtx(ctx2, psrv.URL, "p1", time.Second, nil); err != nil {
			t.Errorf("cancelled pipeline watch: %v", err)
		}
	})

	for _, want := range []string{
		"resume with mytool status job1",
		"resume with mytool pipeline show p1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("watch output missing %q; got:\n%s", want, out)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written during the run.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestShowProgressContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "job1", IsRunning: true, Percent: 50})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := ShowProgressCtx(ctx, srv.URL, "job1", true, true)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cancelled watch returned error %v, want benign nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ShowProgressCtx did not return after context cancellation")
	}
}
