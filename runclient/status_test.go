package runclient

// Job-status semantics: a job's own ErrorMsg is data, not a call
// failure; "not found" and "unauthorized" are permanent and end a watch
// at once; a failed job is reported as ErrJobFailed with its record
// intact; a cancelled watch never looks like a finished job.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// statusServer answers /api/status/<id> with the given handler and
// /api/run with a freshly submitted record for job "j1".
func statusServer(t *testing.T, status func(w http.ResponseWriter, hit int64)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/run":
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", Handler: "img:1", Status: api.StatusQueued})
		case strings.HasPrefix(r.URL.Path, "/api/status/"):
			status(w, hits.Add(1))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func failedRecord() api.JobStatus {
	return api.JobStatus{JobID: "j1", Handler: "img:1", Done: true, Status: api.StatusFailed,
		Log: "line1\nline2", ErrorMsg: "boom", RunningTime: "2s"}
}

// TestGetStatusFailedJobIsData pins that a failed job's record comes
// back with a nil error: the record (Log, ErrorMsg) is what callers
// show; turning ErrorMsg into an error lost the log and made watches
// back off for minutes.
func TestGetStatusFailedJobIsData(t *testing.T) {
	srv, _ := statusServer(t, func(w http.ResponseWriter, _ int64) {
		json.NewEncoder(w).Encode(failedRecord())
	})
	st, err := GetStatus(srv.URL, "j1")
	if err != nil {
		t.Fatalf("GetStatus on a failed job: %v", err)
	}
	if st.Log != "line1\nline2" || st.ErrorMsg != "boom" {
		t.Fatalf("record not returned intact: %+v", st)
	}
	if err := JobError(st); !errors.Is(err, ErrJobFailed) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("JobError = %v, want ErrJobFailed carrying the message", err)
	}
}

// TestGetStatusNotFound pins both not-found shapes: the 404 current
// runners send and the 200 record older runners send.
func TestGetStatusNotFound(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, _ int64){
		"404": func(w http.ResponseWriter, _ int64) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", ErrorMsg: "Did not find job with ID: j1"})
		},
		"legacy-200": func(w http.ResponseWriter, _ int64) {
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", DeployManager: "x", ErrorMsg: "Did not find job with ID: j1"})
		},
		"empty-200": func(w http.ResponseWriter, _ int64) {
			w.Write([]byte("{}"))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := statusServer(t, h)
			if _, err := GetStatus(srv.URL, "j1"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestShowProgressFailedJobEndsPromptly pins that a job that finishes
// failed ends the watch on the first poll (it used to back off ~151 s
// and then claim the job was "still on" the runner).
func TestShowProgressFailedJobEndsPromptly(t *testing.T) {
	srv, hits := statusServer(t, func(w http.ResponseWriter, _ int64) {
		json.NewEncoder(w).Encode(failedRecord())
	})
	st, err := ShowProgressCtx(context.Background(), srv.URL, "j1", true, true)
	if err != nil {
		t.Fatalf("watch of a failed job errored: %v", err)
	}
	if !st.Done || st.ErrorMsg != "boom" || st.Log == "" {
		t.Fatalf("final record = %+v", st)
	}
	if hits.Load() != 1 {
		t.Fatalf("status polled %d times, want 1", hits.Load())
	}
}

// TestShowProgressErrorMarkerKeepsWatching pins that an ErrorMsg on a
// job that is still running (an error marker) does not end the watch.
func TestShowProgressErrorMarkerKeepsWatching(t *testing.T) {
	srv, _ := statusServer(t, func(w http.ResponseWriter, hit int64) {
		if hit < 3 {
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", Handler: "img:1", IsRunning: true,
				Status: api.StatusRunning, ErrorMsg: "WARNING: Incorrect marker command"})
			return
		}
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", Handler: "img:1", Done: true, Status: api.StatusDone, OutputHash: "out"})
	})
	st, err := ShowProgressCtx(context.Background(), srv.URL, "j1", true, true)
	if err != nil || st.OutputHash != "out" {
		t.Fatalf("watch = %+v, %v; want the done record", st, err)
	}
}

// TestShowProgressPermanentErrorsStopAtOnce pins that 404 and 401 are
// not retried with backoff.
func TestShowProgressPermanentErrorsStopAtOnce(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		srv, hits := statusServer(t, func(w http.ResponseWriter, _ int64) {
			w.WriteHeader(code)
		})
		start := time.Now()
		_, err := ShowProgressCtx(context.Background(), srv.URL, "j1", true, true)
		if err == nil || !permanent(err) {
			t.Fatalf("%d: err = %v, want a permanent error", code, err)
		}
		if hits.Load() != 1 || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("%d: polled %d times in %v, want one immediate poll", code, hits.Load(), time.Since(start))
		}
	}
}

// TestStartRunFailedJobKeepsRecord pins that StartRun reports a failed
// job as ErrJobFailed and still hands back the record (JobID, Log,
// ErrorMsg) instead of an empty JobStatus.
func TestStartRunFailedJobKeepsRecord(t *testing.T) {
	srv, _ := statusServer(t, func(w http.ResponseWriter, _ int64) {
		json.NewEncoder(w).Encode(failedRecord())
	})
	st, err := StartRunCtx(context.Background(), srv.URL, true, api.JobRequest{Handler: "img:1"})
	if !errors.Is(err, ErrJobFailed) {
		t.Fatalf("err = %v, want ErrJobFailed", err)
	}
	if st.JobID != "j1" || st.Log != "line1\nline2" || st.ErrorMsg != "boom" {
		t.Fatalf("record lost: %+v", st)
	}
}

// TestStartRunCancelledWatchIsError pins that a cancelled watch is an
// error wrapping context.Canceled, with the submitted record: callers
// used to treat it as success and download an empty OutputHash.
func TestStartRunCancelledWatchIsError(t *testing.T) {
	srv, _ := statusServer(t, func(w http.ResponseWriter, _ int64) {
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", Handler: "img:1", IsRunning: true, Status: api.StatusRunning})
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	st, err := StartRunCtx(ctx, srv.URL, true, api.JobRequest{Handler: "img:1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if st.JobID != "j1" || st.Done {
		t.Fatalf("record = %+v, want the not-done j1 record", st)
	}
}

// TestShowProgressCancelAbortsInFlightPoll pins that cancelling the
// context aborts a status request that hangs, instead of waiting out
// the 30 s request timeout.
func TestShowProgressCancelAbortsInFlightPoll(t *testing.T) {
	release := make(chan struct{})
	srv, _ := statusServer(t, func(w http.ResponseWriter, _ int64) { <-release })
	t.Cleanup(func() { close(release) }) // LIFO: unblock the handler before srv.Close
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := ShowProgressCtx(ctx, srv.URL, "j1", true, true); err != nil {
		t.Fatalf("cancelled watch: %v (want the benign nil)", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %v; the in-flight poll was not aborted", d)
	}
}

// TestCallRunnerCtxCancels pins that CallRunnerCtx honours ctx.
func TestCallRunnerCtxCancels(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // LIFO: unblock the handler before srv.Close
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := CallRunnerCtx(ctx, http.MethodPost, srv.URL, "/api/deploymanager", nil, nil, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the ctx deadline", err)
	}
}

// TestFindJobPicksOwner pins that FindJob skips runners that answer
// not-found (both the 404 and the legacy 200 shape) and returns the
// owner, even when the owner is not first.
func TestFindJobPicksOwner(t *testing.T) {
	legacy, _ := statusServer(t, func(w http.ResponseWriter, _ int64) {
		json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", ErrorMsg: "Did not find job with ID: j1"})
	})
	modern, _ := statusServer(t, func(w http.ResponseWriter, _ int64) { w.WriteHeader(http.StatusNotFound) })
	owner, _ := statusServer(t, func(w http.ResponseWriter, _ int64) { json.NewEncoder(w).Encode(failedRecord()) })
	hit, err := FindJob([]string{legacy.URL, modern.URL, owner.URL}, "j1")
	if err != nil {
		t.Fatalf("FindJob: %v", err)
	}
	if hit.Server != owner.URL || hit.Value.ErrorMsg != "boom" {
		t.Fatalf("hit = %+v, want the owner with its failed record", hit)
	}

	if _, err := FindJob([]string{legacy.URL, modern.URL}, "j1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nowhere: err = %v, want ErrNotFound", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	down.Close()
	if _, err := FindJob([]string{legacy.URL, down.URL}, "j1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("unreachable: err = %v, want a hard error that is not ErrNotFound", err)
	}
}

// TestWaitPipelineCancelledAndPermanent pins that a pipeline that ends
// cancelled is an error, that a 404 ends the wait at once, and that a
// cancelled wait returns the last state seen rather than a zero value.
func TestWaitPipelineCancelledAndPermanent(t *testing.T) {
	serve := func(h func(w http.ResponseWriter)) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(w) }))
		t.Cleanup(srv.Close)
		return srv
	}
	cancelled := serve(func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: api.StatusCancelled})
	})
	if p, err := WaitPipelineCtx(context.Background(), cancelled.URL, "p1", time.Millisecond, nil); err == nil || p.PipelineID != "p1" {
		t.Fatalf("cancelled pipeline: %+v, %v; want its record and an error", p, err)
	}

	var hits atomic.Int64
	missing := serve(func(w http.ResponseWriter) { hits.Add(1); w.WriteHeader(http.StatusNotFound) })
	if _, err := WaitPipelineCtx(context.Background(), missing.URL, "p1", time.Millisecond, nil); !errors.Is(err, ErrNotFound) || hits.Load() != 1 {
		t.Fatalf("404: err=%v after %d polls, want ErrNotFound after 1", err, hits.Load())
	}

	running := serve(func(w http.ResponseWriter) {
		json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: api.StatusRunning})
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	p, err := WaitPipelineCtx(ctx, running.URL, "p1", 5*time.Millisecond, nil)
	if err != nil || p.PipelineID != "p1" {
		t.Fatalf("cancelled wait: %+v, %v; want the last state and nil", p, err)
	}
}
