package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fixtureRoundTrips maps each golden file to the type that must decode it.
// Fixtures are what a server marshals, so decoding is strict: an unknown
// key means the fixture drifted ahead of the code. Re-marshaling and
// re-decoding must produce an identical tree: no field may vanish, appear,
// or change shape on the way back out.
var fixtureRoundTrips = map[string]func() any{
	"job-request.json":      func() any { return new(JobRequest) },
	"job-status.json":       func() any { return new(JobStatus) },
	"job-zero.json":         func() any { return new(JobStatus) },
	"jobs-list.json":        func() any { return new([]JobStatus) },
	"capabilities.json":     func() any { return new(CapabilityReply) },
	"fleet.json":            func() any { return new(FleetReply) },
	"deploy-request.json":   func() any { return new(DeployRequest) },
	"deploy-response.json":  func() any { return new(DeployResponse) },
	"filehost-reply.json":   func() any { return new(DeployResponse) },
	"services.json":         func() any { return new(map[string]TaskInfo) },
	"images.json":           func() any { return new([]ImageInfo) },
	"stats.json":            func() any { return new(JobStats) },
	"pipeline-request.json": func() any { return new(PipelineRequest) },
	"pipeline.json":         func() any { return new(Pipeline) },
	"resume-request.json":   func() any { return new(ResumeRequest) },
	"register-request.json": func() any { return new(RegisterRequest) },
	"register-reply.json":   func() any { return new(RegisterReply) },
	"fleet-hosts.json":      func() any { return new([]FleetHost) },
}

func TestFixturesRoundTrip(t *testing.T) {
	for file, newTarget := range fixtureRoundTrips {
		t.Run(file, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner", file))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(src))
			dec.DisallowUnknownFields()
			target := newTarget()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("fixture does not decode: %v", err)
			}
			out, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(src, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("round-trip changed the document:\nfixture: %s\nremarshal: %s", src, out)
			}
		})
	}
}

// TestFrozenStatusValues pins the wire spellings of status vocabularies:
// fixtures and constants must agree.
func TestFrozenStatusValues(t *testing.T) {
	checks := []struct {
		name   string
		got    string
		frozen string
	}{
		{"job-status fixture", mustStatus(t, "job-status.json"), StatusDone},
	}
	for _, c := range checks {
		if c.got != c.frozen {
			t.Errorf("%s: got %q, want frozen value %q", c.name, c.got, c.frozen)
		}
	}
	if StatusQueued != "queued" || StatusRunning != "running" || StatusDone != "done" ||
		StatusFailed != "failed" || StatusCancelled != "cancelled" {
		t.Error("job status constants drifted from their frozen wire values")
	}
	if StepPending != "pending" || StepRunning != "running" || StepDone != "done" ||
		StepFailed != "failed" || StepSkipped != "skipped" {
		t.Error("step status constants drifted from their frozen wire values")
	}
	if HostPending != "pending" || HostActive != "active" || HostRemoved != "removed" {
		t.Error("fleet host status constants drifted from their frozen wire values")
	}
}

func mustStatus(t *testing.T, file string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner", file))
	if err != nil {
		t.Fatal(err)
	}
	var s JobStatus
	if err := json.Unmarshal(src, &s); err != nil {
		t.Fatal(err)
	}
	return s.Status
}

// TestConfigEchoKeySet pins the top-level configuration keys echoed by
// POST /api/fleet/register. Additions are compatible (decoders ignore
// unknowns); a removal or rename is a protocol change.
func TestConfigEchoKeySet(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "runner", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(src, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Address", "TLSCert", "TLSKey", "Filehost", "Store", "DockerRegistry",
		"Capabilities", "Backends", "BuildArgs", "DeployedImages", "DebugLogging",
		"JobDBPath", "DefaultJobMemoryMB", "TotalCPU", "TotalRAMMB", "TotalGPU",
		"JobTypeSpecs", "AdminToken", "FleetJoinToken", "FleetSeed", "FileServer",
	}
	if len(doc) != len(want) {
		t.Errorf("config key count: got %d, want %d (keys: %v)", len(doc), len(want), keysOf(doc))
	}
	for _, k := range want {
		if _, ok := doc[k]; !ok {
			t.Errorf("config fixture lost key %q", k)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRoutesManifest pins the Route table against testdata/runner/routes.json
// in both directions. To refresh the manifest after a deliberate route
// change: GEN_ROUTES=1 go test ./runner/api -run TestRoutesManifest
func TestRoutesManifest(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "runner", "routes.json")
	if os.Getenv("GEN_ROUTES") == "1" {
		b, err := json.MarshalIndent(Routes, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("routes.json regenerated")
		return
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing routes manifest: %v (regenerate with GEN_ROUTES=1)", err)
	}
	var want []Route
	if err := json.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Routes, want) {
		t.Errorf("Routes table and routes.json drifted apart:\ncode:    %+v\nfixture: %+v", Routes, want)
	}
}
