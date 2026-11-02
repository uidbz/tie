package api

import (
	"encoding/json"

	"github.com/uidbz/tie/io/compose"
)

// PipelineRequest is a whole step graph submitted in one call. The runner
// owns sequencing from there: it composes each step's input from its
// predecessors' outputs and submits the job as soon as the step's
// dependencies are satisfied, so the client can exit the moment submission
// returns.
//
// Steps address each other by name through Spec.FromStep, using the same
// selection vocabulary the client-side composition helpers emit — one
// evaluator, shared via io/compose, so a pipeline is not written twice.
type PipelineRequest struct {
	Name  string
	User  string
	Debug bool
	// Seeds pre-satisfies a step name with a tree hash the client already
	// uploaded, so a step's Inputs can name it like any other predecessor.
	Seeds map[string]string
	Steps []PipelineStep
}

// PipelineStep is one job plus the selection that builds its input.
// Everything from Ordinal down is runtime state owned by the runner and
// ignored on submission.
type PipelineStep struct {
	Name     string
	Handler  string
	JobType  string
	AppHash  string
	Settings json.RawMessage
	Inputs   []compose.Spec

	Ordinal      int
	Status       string
	JobID        string
	OutputHash   string
	ErrorMsg     string
	StartedUnix  int64
	FinishedUnix int64
}

// Pipeline is the persisted state of one run. Its Status reuses the job
// status vocabulary (running/done/failed/cancelled) so clients need no
// second one.
type Pipeline struct {
	PipelineID    string
	Name          string
	User          string
	Status        string
	ErrorMsg      string
	DeployManager string
	Debug         bool
	CreatedUnix   int64
	FinishedUnix  int64
	Steps         []*PipelineStep
}

// Step status values. Running spans submission through the job's terminal
// state. Skipped means the step could not run because a required input never
// appeared — deliberately distinct from Failed: the step's own job never
// started.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// ResumeRequest reruns part of a finished pipeline (POST
// /api/pipelines/{id}/resume). From names the first step to rerun;
// everything downstream of it reruns too. With reads a step's output from a
// hash the caller supplies instead of rerunning it. An empty body — the
// common case, "retry what did not finish" — is valid.
type ResumeRequest struct {
	From string
	With map[string]string
}
