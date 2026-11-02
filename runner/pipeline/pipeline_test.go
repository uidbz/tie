package pipeline

import (
	"strings"
	"testing"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/runner/api"
)

// The step graph is derived from the selection, ordered, and then walked
// entirely in memory, so everything below runs without a database or a
// filehost. Selection semantics themselves are pinned in io/compose.

func from(stepName string, paths ...string) compose.Spec {
	return compose.Spec{FromStep: stepName, Paths: paths}
}

func optionalFrom(stepName string, paths ...string) compose.Spec {
	s := from(stepName, paths...)
	s.Optional = true
	return s
}

func stepOf(name, jobType string, inputs ...compose.Spec) api.PipelineStep {
	return api.PipelineStep{Name: name, Handler: name + ":latest", JobType: jobType, Inputs: inputs}
}

const (
	jobType  = "CPUv1"
	seedHash = "0000000000000000000000000000000000000000000000000000000000000abc"
)

// knownTypes mimics the runner-side validator: every job type this fake
// runner can execute.
func knownTypes(req api.JobRequest) error {
	if req.JobType != jobType {
		return errUnknownJobType{req.JobType}
	}
	return nil
}

type errUnknownJobType struct{ typ string }

func (e errUnknownJobType) Error() string { return "unknown job type: " + e.typ }

func testEngine() *Engine {
	return New(Config{ValidateStep: knownTypes})
}

func stepNames(steps []*api.PipelineStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

// A step must be ordered after every step it selects from, whatever
// order it was submitted in, because advance scans in ordinal order.
func TestBuildOrdersStepsAfterTheirInputs(t *testing.T) {
	// Submitted deliberately backwards.
	p, err := testEngine().build(&api.PipelineRequest{
		Seeds: map[string]string{"raw": seedHash},
		Steps: []api.PipelineStep{
			stepOf("report", jobType, from("cluster", "a.csv"), from("score", "b.csv")),
			stepOf("score", jobType, from("cluster", "a.csv")),
			stepOf("cluster", jobType, from("raw", "in.dat")),
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	got := stepNames(p.Steps)
	want := []string{"raw", "cluster", "score", "report"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got order %v, want %v", got, want)
	}

	// The seed is recorded already satisfied so readiness needs no
	// special case.
	seed := step(p, "raw")
	if !isSeed(seed) || !satisfied(seed) || seed.OutputHash != seedHash {
		t.Fatalf("seed not recorded as a satisfied input: %+v", seed)
	}
	if seed.Ordinal >= step(p, "cluster").Ordinal {
		t.Error("seed must sort before the steps that read it")
	}
}

// Independent steps must keep their submission order, so a pipeline
// reads the way its author wrote it when the graph leaves the order free.
func TestBuildKeepsIndependentStepsInSubmissionOrder(t *testing.T) {
	p, err := testEngine().build(&api.PipelineRequest{
		Steps: []api.PipelineStep{
			stepOf("cluster", jobType),
			stepOf("search", jobType, from("cluster", "a.csv")),
			stepOf("score", jobType, from("cluster", "a.csv")),
			stepOf("embeddings", jobType, from("cluster", "a.csv")),
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cluster", "search", "score", "embeddings"}
	if got := stepNames(p.Steps); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got order %v, want %v", got, want)
	}
}

// A cycle would leave every step waiting forever with the pipeline stuck
// running, so it has to be caught at submission and name the steps
// involved.
func TestBuildRejectsCycles(t *testing.T) {
	_, err := testEngine().build(&api.PipelineRequest{
		Steps: []api.PipelineStep{
			stepOf("a", jobType, from("c", "x.csv")),
			stepOf("b", jobType, from("a", "x.csv")),
			stepOf("c", jobType, from("b", "x.csv")),
		},
	}, "")
	if err == nil {
		t.Fatal("expected a cycle to be rejected")
	}
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("cycle error should name step %q: %v", name, err)
		}
	}
}

func TestBuildRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name string
		req  api.PipelineRequest
		want string
	}{
		{"no steps", api.PipelineRequest{}, "no steps"},
		{
			"unknown input step",
			api.PipelineRequest{Steps: []api.PipelineStep{stepOf("a", jobType, from("nope", "x.csv"))}},
			"unknown step",
		},
		{
			"duplicate name",
			api.PipelineRequest{Steps: []api.PipelineStep{stepOf("a", jobType), stepOf("a", jobType)}},
			"duplicate step name",
		},
		{
			"name collides with a seed",
			api.PipelineRequest{
				Seeds: map[string]string{"a": seedHash},
				Steps: []api.PipelineStep{stepOf("a", jobType)},
			},
			"duplicate step name",
		},
		{
			"unknown job type",
			api.PipelineRequest{Steps: []api.PipelineStep{stepOf("a", "NoSuchType")}},
			"unknown job type",
		},
		{
			"no handler",
			api.PipelineRequest{Steps: []api.PipelineStep{{Name: "a", JobType: jobType}}},
			"no handler",
		},
		{
			"seed hash wrong length",
			api.PipelineRequest{
				Seeds: map[string]string{"raw": "abc"},
				Steps: []api.PipelineStep{stepOf("a", jobType, from("raw", "x.csv"))},
			},
			"invalid hash",
		},
		{
			"input names itself",
			api.PipelineRequest{Steps: []api.PipelineStep{stepOf("a", jobType, from("a", "x.csv"))}},
			"names itself",
		},
		{
			// Hash wins in ResolveSteps while FromStep still creates a graph
			// edge, so the step would wait on a predecessor whose output it
			// never reads.
			"both step and hash",
			api.PipelineRequest{Steps: []api.PipelineStep{
				stepOf("a", jobType),
				stepOf("b", jobType, compose.Spec{FromStep: "a", Hash: seedHash, Paths: []string{"x.csv"}}),
			}},
			"use one",
		},
		{
			"input with neither step nor hash",
			api.PipelineRequest{Steps: []api.PipelineStep{
				stepOf("a", jobType, compose.Spec{Paths: []string{"x.csv"}}),
			}},
			"neither a step nor a hash",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testEngine().build(&tc.req, "")
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A step waits while any dependency is still in flight -- optional
// included, since until it is terminal there is no way to know whether
// its output will exist.
func TestStepReadinessWaitsForUnfinishedDependencies(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "cluster", Status: api.StepRunning},
		{Name: "score", Status: api.StepPending, Inputs: []compose.Spec{optionalFrom("cluster", "a.csv")}},
	}}
	if got, _ := stepReadiness(p, step(p, "score")); got != stepWaiting {
		t.Fatalf("got %v, want stepWaiting for an in-flight optional dependency", got)
	}
}

func TestStepReadinessReadyOnlyWhenEveryDependencyIsResolved(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "cluster", Status: api.StepDone, OutputHash: seedHash},
		{Name: "extra", Status: api.StepSkipped},
		{Name: "report", Status: api.StepPending, Inputs: []compose.Spec{
			from("cluster", "a.csv"),
			optionalFrom("extra", "r_extra.tsv"),
		}},
	}}
	if got, reason := stepReadiness(p, step(p, "report")); got != stepReady {
		t.Fatalf("got %v (%s), want stepReady: a skipped optional input must not block", got, reason)
	}
}

// A step whose required input will never arrive is skipped rather than
// left pending, which is what lets the pipeline reach a terminal state.
func TestStepReadinessSkipsWhenRequiredInputIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		dep  *api.PipelineStep
		want string
	}{
		{"failed", &api.PipelineStep{Name: "cluster", Status: api.StepFailed}, "failed"},
		{"skipped", &api.PipelineStep{Name: "cluster", Status: api.StepSkipped}, "was skipped"},
		// Done but with no output hash: a side-effect-only step is
		// legitimate, but there is nothing for a dependent to select from.
		{"no output", &api.PipelineStep{Name: "cluster", Status: api.StepDone}, "produced no output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &api.Pipeline{Steps: []*api.PipelineStep{
				tc.dep,
				{Name: "score", Status: api.StepPending, Inputs: []compose.Spec{from("cluster", "a.csv")}},
			}}
			got, reason := stepReadiness(p, step(p, "score"))
			if got != stepUnrunnable {
				t.Fatalf("got %v, want stepUnrunnable", got)
			}
			if !strings.Contains(reason, tc.want) || !strings.Contains(reason, "cluster") {
				t.Fatalf("reason %q should name cluster and say %q", reason, tc.want)
			}
		})
	}
}

// Resuming from a step must rerun everything downstream of it, including
// steps that succeeded: their input is composed from a predecessor that
// is about to produce a possibly different output hash.
func TestStepsToRerunIncludesDownstreamAndExcludesSeeds(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "raw", Status: api.StepDone, OutputHash: seedHash},
		{Name: "cluster", Status: api.StepDone, OutputHash: seedHash, Handler: "cluster:latest",
			Inputs: []compose.Spec{from("raw", "in.dat")}},
		{Name: "score", Status: api.StepDone, OutputHash: seedHash, Handler: "score:latest",
			Inputs: []compose.Spec{from("cluster", "a.csv")}},
		{Name: "report", Status: api.StepFailed, Handler: "report:latest",
			Inputs: []compose.Spec{from("score", "b.csv")}},
	}}

	rerun := stepsToRerun(p, "cluster")
	for _, name := range []string{"cluster", "score", "report"} {
		if !rerun[name] {
			t.Errorf("resuming from cluster must rerun %q", name)
		}
	}
	if rerun["raw"] {
		t.Error("a seed is client-supplied and must never be rerun")
	}
}

// With no step named, resume retries what did not complete -- and only
// what depends on it, so completed upstream work is reused.
func TestStepsToRerunWithoutFromStepRetriesOnlyIncompleteWork(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "cluster", Status: api.StepDone, OutputHash: seedHash, Handler: "cluster:latest"},
		{Name: "score", Status: api.StepFailed, Handler: "score:latest",
			Inputs: []compose.Spec{from("cluster", "a.csv")}},
		{Name: "report", Status: api.StepSkipped, Handler: "report:latest",
			Inputs: []compose.Spec{from("score", "b.csv")}},
	}}

	rerun := stepsToRerun(p, "")
	if rerun["cluster"] {
		t.Error("a completed step must be reused, not rerun")
	}
	if !rerun["score"] || !rerun["report"] {
		t.Errorf("both the failed step and its skipped dependent must rerun: %v", rerun)
	}
}

func TestEntryHandler(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Name: "raw", Ordinal: -1, Status: api.StepDone, OutputHash: seedHash},
		{Name: "second", Ordinal: 1, Status: api.StepRunning, Handler: "second:latest"},
		{Name: "first", Ordinal: 0, Status: api.StepRunning, Handler: "first:latest"},
	}}
	if got := EntryHandler(p); got != "first:latest" {
		t.Errorf("EntryHandler = %q, want the lowest-ordinal job step", got)
	}
	seedsOnly := &api.Pipeline{Steps: []*api.PipelineStep{{Name: "raw", Status: api.StepDone, OutputHash: seedHash}}}
	if got := EntryHandler(seedsOnly); got != "" {
		t.Errorf("EntryHandler of a seed-only pipeline = %q, want empty", got)
	}
}
