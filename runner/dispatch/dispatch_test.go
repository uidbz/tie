package dispatch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
)

func capServer(t *testing.T, reply api.CapabilityReply) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capabilities" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(reply)
	}))
}

func TestSelect(t *testing.T) {
	idle := api.CapabilityReply{Capabilities: []string{"CPUv1"}, FreeCPU: 8, TotalCPU: 16, IsAvailable: true}
	full := api.CapabilityReply{Capabilities: []string{"CPUv1"}, FreeCPU: 0, TotalCPU: 16, IsAvailable: false}
	noCap := api.CapabilityReply{Capabilities: []string{"GPUv1"}, FreeCPU: 8, TotalCPU: 16, IsAvailable: true}
	legacy := api.CapabilityReply{Capabilities: []string{"CPUv1"}, IsAvailable: true} // pre-capacity runner

	idleSrv := capServer(t, idle)
	defer idleSrv.Close()
	fullSrv := capServer(t, full)
	defer fullSrv.Close()
	noCapSrv := capServer(t, noCap)
	defer noCapSrv.Close()
	legacySrv := capServer(t, legacy)
	defer legacySrv.Close()

	spec := func(string) (executor.Spec, bool) { return executor.Spec{CPU: 4}, true }

	t.Run("local capable and has room runs local", func(t *testing.T) {
		d := &Router{Capabilities: func() []string { return []string{"CPUv1"} }, ResolveSpec: spec, CanFit: func(executor.Spec) bool { return true }}
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != "" || !local || fq {
			t.Errorf("got %q local=%v fq=%v", target, local, fq)
		}
	})

	t.Run("local full forwards to first backend with room", func(t *testing.T) {
		d := &Router{
			Capabilities: func() []string { return []string{"CPUv1"} },
			Backends:     func() []string { return []string{fullSrv.URL, idleSrv.URL} },
			ResolveSpec:  spec,
			CanFit:       func(executor.Spec) bool { return false },
		}
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != idleSrv.URL || local || fq {
			t.Errorf("got %q local=%v fq=%v, want forward to idle", target, local, fq)
		}
	})

	t.Run("forceAddToQueue keeps local queueing", func(t *testing.T) {
		d := &Router{Capabilities: func() []string { return []string{"CPUv1"} }, ResolveSpec: spec, CanFit: func(executor.Spec) bool { return true }}
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1", ForceAddToQueue: true}, "http://self")
		if target != "" || !local || !fq {
			t.Errorf("got %q local=%v fq=%v", target, local, fq)
		}
	})

	t.Run("no runner idle round-robins forced queue across capable runners", func(t *testing.T) {
		d := &Router{
			Capabilities: func() []string { return []string{"CPUv1"} },
			Backends:     func() []string { return []string{fullSrv.URL} },
			ResolveSpec:  spec,
			CanFit:       func(executor.Spec) bool { return false },
		}
		// suitable = [self, fullSrv]; counter 1 -> index 1 (backend),
		// counter 2 -> index 0 (self forced-queue).
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != fullSrv.URL || local || !fq {
			t.Errorf("round 1: got %q local=%v fq=%v", target, local, fq)
		}
		target, local, fq = d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != "" || !local || !fq {
			t.Errorf("round 2: got %q local=%v fq=%v, want self forced-queue", target, local, fq)
		}
	})

	t.Run("incapable everywhere rejects", func(t *testing.T) {
		d := &Router{
			Capabilities: func() []string { return nil }, // local not capable either
			Backends:     func() []string { return []string{noCapSrv.URL} },
			ResolveSpec:  spec,
			CanFit:       func(executor.Spec) bool { return false },
		}
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != "" || local || fq {
			t.Errorf("no suitable backend must reject: %q %v %v", target, local, fq)
		}
	})

	t.Run("legacy backend without capacity fields falls back to IsAvailable", func(t *testing.T) {
		d := &Router{
			Capabilities: func() []string { return []string{"CPUv1"} },
			Backends:     func() []string { return []string{legacySrv.URL} },
			ResolveSpec:  spec,
			CanFit:       func(executor.Spec) bool { return false },
		}
		target, local, fq := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != legacySrv.URL || local || fq {
			t.Errorf("got %q local=%v fq=%v", target, local, fq)
		}
	})

	t.Run("unreachable backend is never capable", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		dead.Close()
		d := &Router{
			Capabilities: func() []string { return nil },
			Backends:     func() []string { return []string{dead.URL} },
			ResolveSpec:  spec,
			CanFit:       func(executor.Spec) bool { return false },
		}
		target, local, _ := d.Select(api.JobRequest{JobType: "CPUv1"}, "http://self")
		if target != "" || local {
			t.Errorf("dead backend must not be chosen: %q %v", target, local)
		}
	})
}

func TestForward(t *testing.T) {
	var gotAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = fmt.Sprint(r.Method, " ", r.URL.Path, " ", r.URL.Query().Get("x"))
		u, p, _ := r.BasicAuth()
		w.Header().Set("X-Echo", "yes")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "body %s:%s", u, p)
	}))
	defer backend.Close()

	req, _ := http.NewRequest(http.MethodPut, "http://front/api/run?x=7", nil)
	req.RequestURI = "/api/run?x=7" // what an incoming server request carries
	req.SetBasicAuth("tie", "tok")
	rec := httptest.NewRecorder()
	body := Forward(nil, backend.URL, rec, req)

	if gotAuth != "PUT /api/run 7" {
		t.Errorf("backend saw %q", gotAuth)
	}
	if rec.Code != http.StatusCreated || rec.Header().Get("X-Echo") != "yes" {
		t.Errorf("status/headers not passed through: %d %v", rec.Code, rec.Header())
	}
	if string(body) != "body tie:tok" {
		t.Errorf("returned body = %q", body)
	}
}
