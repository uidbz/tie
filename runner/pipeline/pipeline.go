// Package pipeline sequences multi-step runs. A pipeline is a step graph
// submitted in one call; each step is a job whose input tree is composed
// from its predecessors' outputs via io/compose, so only manifests move:
// no job output is ever downloaded, copied or re-uploaded.
//
// The engine is store-driven and idempotent: every advance re-reads the
// pipeline and the job outcomes from the store, so one hook (a job
// finishing), submission, restart recovery, and a manual resume all
// share the same advance path, and a runner restart loses nothing.
// Submitting *all* ready steps per pass is what gives the graph real
// parallelism; the scheduler's capacity admission decides what actually
// starts.
//
// Job submission and cancellation go through injected callbacks: the
// engine never knows whether a step runs on the local queue or an
// external backend — that dispatch is the runner's.
package pipeline

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/store"
)

// Config wires the engine to its collaborators.
type Config struct {
	// Store is the durable source of truth for pipelines and job
	// outcomes. Required.
	Store store.Store
	// SubmitJob turns a step into a job (the runner dispatches it to the
	// local queue or an external backend) and returns the created job.
	// Required.
	SubmitJob func(req api.JobRequest, deployManager string) (*api.JobStatus, error)
	// CancelJob cancels a live job by id (a pipeline cancel stops its
	// in-flight steps). Required.
	CancelJob func(jobID string) error
	// ValidateStep optionally rejects a step this runner can never
	// execute (unknown job type, over capacity, backend disabled).
	ValidateStep func(req api.JobRequest) error
	// Filehost and StoreName are the endpoints compose.Build uses to
	// fetch predecessor manifests and upload the composed tree.
	Filehost  string
	StoreName string
}

// Engine advances stored pipelines. The zero value is not usable; New it.
type Engine struct {
	cfg   Config
	locks sync.Map // pipelineID -> *sync.Mutex; per-pipeline because advancing fetches manifests over HTTP: one unreachable filehost must not wedge every other run
}

// New builds an engine.
func New(cfg Config) *Engine { return &Engine{cfg: cfg} }

func (e *Engine) lock(pipelineID string) *sync.Mutex {
	mu, _ := e.locks.LoadOrStore(pipelineID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// isSeed reports whether a step is a client-supplied input rather than a
// job to run. Seeds carry no handler and are recorded already done.
func isSeed(s *api.PipelineStep) bool { return s.Handler == "" }

// terminal reports whether a step will never change state again.
func terminal(s *api.PipelineStep) bool {
	return s.Status == api.StepDone || s.Status == api.StepFailed || s.Status == api.StepSkipped
}

// satisfied reports whether this step can serve as an input to another. A
// step that finished without reporting an output hash is not: there is
// nothing to select from. Side-effect-only steps are therefore Done but
// unsatisfying, which their required dependents report as a missing
// input.
func satisfied(s *api.PipelineStep) bool {
	return s.Status == api.StepDone && s.OutputHash != ""
}

// step returns the named step, or nil.
func step(p *api.Pipeline, name string) *api.PipelineStep {
	for _, s := range p.Steps {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// EntryHandler is the image of the lowest-ordinal job step, which stands
// for the whole run when authorizing against a per-image key. Steps can
// carry different images with different keys, so picking the entry step
// keeps the answer stable: it does not depend on which step happens to
// be running at the time. Seeds are skipped — they have no image, and
// their ordinals are negative.
func EntryHandler(p *api.Pipeline) string {
	var entry *api.PipelineStep
	for _, s := range p.Steps {
		if isSeed(s) {
			continue
		}
		if entry == nil || s.Ordinal < entry.Ordinal {
			entry = s
		}
	}
	if entry == nil {
		return ""
	}
	return entry.Handler
}

// Submit validates, persists and starts a pipeline. It returns as soon as
// the initially-ready steps are queued, which is what lets the client
// exit.
func (e *Engine) Submit(req *api.PipelineRequest, deployManager string) (*api.Pipeline, error) {
	p, err := e.build(req, deployManager)
	if err != nil {
		return nil, err
	}
	if err := e.cfg.Store.InsertPipeline(p); err != nil {
		return nil, fmt.Errorf("persisting pipeline: %w", err)
	}
	slog.Info("Pipeline submitted", "pipeline_id", p.PipelineID, "name", p.Name,
		"user", p.User, "steps", len(req.Steps), "seeds", len(req.Seeds))

	e.Advance(p.PipelineID)
	if updated, ok := e.cfg.Store.GetPipeline(p.PipelineID); ok {
		return updated, nil
	}
	return p, nil
}

// build validates a request and returns the pipeline to persist, with
// steps in dependency order.
//
// Everything checkable is checked here rather than at advance time: a
// typo'd step name, an unrunnable job type or a cycle is reported to the
// caller that can still do something about it, instead of stranding a
// pipeline the client has stopped watching.
func (e *Engine) build(req *api.PipelineRequest, deployManager string) (*api.Pipeline, error) {
	if len(req.Steps) == 0 {
		return nil, fmt.Errorf("pipeline has no steps")
	}

	// Seeds and steps share one name space, because Inputs cannot tell
	// them apart.
	seen := make(map[string]bool, len(req.Steps)+len(req.Seeds))
	for name, hash := range req.Seeds {
		if name == "" {
			return nil, fmt.Errorf("seed with an empty name")
		}
		if len(hash) != 64 {
			return nil, fmt.Errorf("seed %q: invalid hash (wrong length): %s", name, hash)
		}
		seen[name] = true
	}

	jobSteps := make(map[string]*api.PipelineStep, len(req.Steps))
	for i := range req.Steps {
		s := &req.Steps[i]
		switch {
		case s.Name == "":
			return nil, fmt.Errorf("step %d has no name", i)
		case seen[s.Name]:
			return nil, fmt.Errorf("duplicate step name %q", s.Name)
		case s.Handler == "":
			return nil, fmt.Errorf("step %q has no handler", s.Name)
		case s.JobType == "":
			return nil, fmt.Errorf("step %q has no job type", s.Name)
		}
		if e.cfg.ValidateStep != nil {
			if err := e.cfg.ValidateStep(api.JobRequest{JobType: s.JobType, Handler: s.Handler}); err != nil {
				return nil, fmt.Errorf("step %q: %w", s.Name, err)
			}
		}
		seen[s.Name] = true
		jobSteps[s.Name] = s
	}

	for _, s := range jobSteps {
		if err := validateStepInputs(s, seen); err != nil {
			return nil, err
		}
	}

	ordered, err := orderSteps(req.Steps, jobSteps)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	p := &api.Pipeline{
		PipelineID:    uuid.NewString(),
		Name:          req.Name,
		User:          req.User,
		Status:        api.StatusRunning,
		DeployManager: deployManager,
		Debug:         req.Debug,
		CreatedUnix:   now,
	}

	// Seeds come first and already done, so readiness treats them like
	// any other satisfied predecessor with no special case anywhere else.
	seedNames := make([]string, 0, len(req.Seeds))
	for name := range req.Seeds {
		seedNames = append(seedNames, name)
	}
	sort.Strings(seedNames)
	for i, name := range seedNames {
		p.Steps = append(p.Steps, &api.PipelineStep{
			Name:         name,
			Ordinal:      -len(seedNames) + i,
			Status:       api.StepDone,
			OutputHash:   req.Seeds[name],
			FinishedUnix: now,
		})
	}
	p.Steps = append(p.Steps, ordered...)
	return p, nil
}

// validateStepInputs rejects a selection the runner could never resolve.
func validateStepInputs(s *api.PipelineStep, known map[string]bool) error {
	for _, in := range s.Inputs {
		switch {
		case in.FromStep != "" && in.Hash != "":
			// Hash would win in ResolveSteps while FromStep still creates
			// a graph edge, so the step would wait on a predecessor it
			// never reads.
			return fmt.Errorf("step %q: input names both step %q and a hash; use one",
				s.Name, in.FromStep)
		case in.FromStep != "":
			if !known[in.FromStep] {
				return fmt.Errorf("step %q: input names unknown step %q", s.Name, in.FromStep)
			}
			if in.FromStep == s.Name {
				return fmt.Errorf("step %q: input names itself", s.Name)
			}
		case len(in.Hash) == 64:
		case in.Hash != "":
			return fmt.Errorf("step %q: invalid input hash (wrong length): %s", s.Name, in.Hash)
		default:
			return fmt.Errorf("step %q: input has neither a step nor a hash", s.Name)
		}
	}
	return nil
}

// orderSteps topologically sorts the job steps so each follows the steps
// it selects from, which both rejects cycles and fixes a stable display
// order. Dependencies come from the selection itself, so the graph can
// never disagree with the inputs that create it.
func orderSteps(steps []api.PipelineStep, jobSteps map[string]*api.PipelineStep) ([]*api.PipelineStep, error) {
	indegree := make(map[string]int, len(steps))
	dependents := make(map[string][]string, len(steps))
	for name := range jobSteps {
		indegree[name] = 0
	}
	for i := range steps {
		s := &steps[i]
		for _, dep := range compose.Dependencies(s.Inputs) {
			if _, isJobStep := jobSteps[dep.Step]; !isJobStep {
				continue // a seed: satisfied before the pipeline starts
			}
			indegree[s.Name]++
			dependents[dep.Step] = append(dependents[dep.Step], s.Name)
		}
	}

	// Seed the frontier in submission order so the result stays close to
	// what the author wrote when the graph leaves the order free.
	var ready []string
	for i := range steps {
		if indegree[steps[i].Name] == 0 {
			ready = append(ready, steps[i].Name)
		}
	}

	ordered := make([]*api.PipelineStep, 0, len(steps))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		s := jobSteps[name]
		s.Ordinal = len(ordered)
		s.Status = api.StepPending
		ordered = append(ordered, s)
		for _, d := range dependents[name] {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
			}
		}
	}

	if len(ordered) != len(steps) {
		var stuck []string
		for name, deg := range indegree {
			if deg > 0 {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("steps form a dependency cycle: %s", strings.Join(stuck, ", "))
	}
	return ordered, nil
}

// JobFinished tells a job's pipeline that the job reached a terminal
// state (wire the scheduler's OnFinished hook to this). Advancing is
// asynchronous because it composes manifests and submits the next steps,
// which must not block the finishing job's goroutine or the poller.
func (e *Engine) JobFinished(job *api.JobStatus) {
	if job.PipelineID != "" {
		go e.Advance(job.PipelineID)
	}
}

// stepState classifies a pending step against its dependencies' current
// state.
type stepState int

const (
	stepWaiting    stepState = iota // a dependency has not finished yet
	stepReady                       // every dependency is resolved: submit it
	stepUnrunnable                  // a required input will never arrive
)

// stepReadiness classifies one pending step. A dependency that is still
// pending or running blocks the step whether it is required or optional:
// until it is terminal there is no way to know whether its output will
// exist.
func stepReadiness(p *api.Pipeline, s *api.PipelineStep) (stepState, string) {
	for _, dep := range compose.Dependencies(s.Inputs) {
		d := step(p, dep.Step)
		if d == nil {
			// Submission validates every FromStep, so reaching this means
			// the stored graph lost a row.
			return stepUnrunnable, fmt.Sprintf("input step %q is missing from the pipeline", dep.Step)
		}
		if !terminal(d) {
			return stepWaiting, ""
		}
		if satisfied(d) || !dep.Required {
			continue
		}
		reason := "did not run"
		switch d.Status {
		case api.StepFailed:
			reason = "failed"
		case api.StepSkipped:
			reason = "was skipped"
		case api.StepDone:
			reason = "produced no output"
		}
		return stepUnrunnable, fmt.Sprintf("required input from step %q %s", dep.Step, reason)
	}
	return stepReady, ""
}

// Advance moves a pipeline forward: it records the outcome of steps whose
// jobs finished, skips steps that can no longer run, and submits every
// step that is now ready.
//
// Submitting *all* ready steps rather than one is what gives the graph
// real parallelism. It costs nothing extra: the scheduler already admits
// by resource fit, and independent steps use different handlers so the
// per-handler gate does not serialize them.
//
// The function is idempotent and safe to call at any time, which is what
// lets one hook (a job finishing), submission, and restart recovery all
// share it.
func (e *Engine) Advance(pipelineID string) {
	mu := e.lock(pipelineID)
	mu.Lock()
	defer mu.Unlock()

	p, ok := e.cfg.Store.GetPipeline(pipelineID)
	if !ok {
		slog.Error("Advance requested for unknown pipeline", "pipeline_id", pipelineID)
		return
	}
	if p.Status != api.StatusRunning {
		return
	}

	e.syncFinishedSteps(p)

	// Skipping a step can make its own dependents unrunnable, and
	// submitting one never unblocks anything, so loop until a pass changes
	// nothing.
	for {
		changed := false
		for _, s := range p.Steps {
			if s.Status != api.StepPending {
				continue
			}
			state, reason := stepReadiness(p, s)
			switch state {
			case stepWaiting:
				continue
			case stepUnrunnable:
				s.Status = api.StepSkipped
				s.ErrorMsg = reason
				s.FinishedUnix = time.Now().Unix()
				slog.Info("Pipeline step skipped", "pipeline_id", pipelineID, "step", s.Name, "reason", reason)
			case stepReady:
				if err := e.submitStep(p, s); err != nil {
					s.Status = api.StepFailed
					s.ErrorMsg = err.Error()
					s.FinishedUnix = time.Now().Unix()
					slog.Error("Pipeline step submission failed", "pipeline_id", pipelineID, "step", s.Name, "error", err)
				} else {
					slog.Info("Pipeline step submitted", "pipeline_id", pipelineID, "step", s.Name,
						"job_id", s.JobID, "handler", s.Handler)
				}
			}
			e.cfg.Store.UpsertPipelineStep(pipelineID, s)
			changed = true
		}
		if !changed {
			break
		}
	}

	e.finalizeIfDone(p)
}

// syncFinishedSteps copies the outcome of each running step's job onto
// the step. The job store is the source of truth rather than an
// in-memory handoff, so a step is attributed correctly no matter which
// executor ran it or whether the runner restarted in between.
func (e *Engine) syncFinishedSteps(p *api.Pipeline) {
	for _, s := range p.Steps {
		if s.Status != api.StepRunning || s.JobID == "" {
			continue
		}
		job, ok := e.cfg.Store.GetJob(s.JobID)
		if !ok {
			continue
		}
		switch job.Status {
		case api.StatusDone:
			s.Status = api.StepDone
			s.OutputHash = job.OutputHash
		case api.StatusFailed, api.StatusCancelled:
			s.Status = api.StepFailed
			s.ErrorMsg = job.ErrorMsg
			if s.ErrorMsg == "" {
				s.ErrorMsg = "job " + job.Status
			}
		default:
			continue // still queued or running
		}
		s.FinishedUnix = job.FinishedUnix
		if s.FinishedUnix == 0 {
			s.FinishedUnix = time.Now().Unix()
		}
		e.cfg.Store.UpsertPipelineStep(p.PipelineID, s)
	}
}

// submitStep composes the step's input tree and submits its job. Only
// manifests move: the composed tree references its predecessors' file
// blobs by hash, so no job output is ever downloaded, copied or
// re-uploaded.
func (e *Engine) submitStep(p *api.Pipeline, s *api.PipelineStep) error {
	specs, err := compose.ResolveSteps(s.Inputs, func(name string) (string, bool) {
		d := step(p, name)
		if d == nil || !satisfied(d) {
			return "", false
		}
		return d.OutputHash, true
	})
	if err != nil {
		return err
	}

	// Zero specs composes the empty directory, which is the right input
	// for a step that generates rather than transforms.
	inputHash, err := compose.Build(e.cfg.Filehost, e.cfg.StoreName, specs)
	if err != nil {
		return err
	}

	job, err := e.cfg.SubmitJob(api.JobRequest{
		JobType:    s.JobType,
		Handler:    s.Handler,
		InputHash:  inputHash,
		AppHash:    s.AppHash,
		Settings:   s.Settings,
		Debug:      p.Debug,
		User:       p.User,
		PipelineID: p.PipelineID,
		StepName:   s.Name,
	}, p.DeployManager)
	if err != nil {
		return err
	}

	s.Status = api.StepRunning
	s.JobID = job.JobID
	s.StartedUnix = time.Now().Unix()
	return nil
}

// finalizeIfDone closes the pipeline once no step can still make
// progress. A run is failed if any step failed or was skipped, so a
// partially completed graph is never reported as success.
func (e *Engine) finalizeIfDone(p *api.Pipeline) {
	var failed []string
	for _, s := range p.Steps {
		if !terminal(s) {
			return
		}
		if s.Status != api.StepDone {
			failed = append(failed, s.Name)
		}
	}

	status, errMsg := api.StatusDone, ""
	if len(failed) > 0 {
		status = api.StatusFailed
		errMsg = "steps did not complete: " + strings.Join(failed, ", ")
	}
	finished := time.Now().Unix()
	e.cfg.Store.UpdatePipelineStatus(p.PipelineID, status, errMsg, finished)
	p.Status, p.ErrorMsg, p.FinishedUnix = status, errMsg, finished
	slog.Info("Pipeline finished", "pipeline_id", p.PipelineID, "name", p.Name,
		"status", status, "steps", len(p.Steps), "not_completed", len(failed))
}

// ResumeAll advances every pipeline a restart interrupted. A run whose
// in-flight local step was killed sees that step fail (the store's
// ReconcileInterrupted marks it) and closes out; one waiting on an
// external job that outlived the restart keeps waiting for the poller;
// and one whose next step was merely pending is genuinely resumed.
func (e *Engine) ResumeAll() {
	pipelines, err := e.cfg.Store.ListRunningPipelines()
	if err != nil {
		slog.Error("Failed to load running pipelines on startup", "error", err)
		return
	}
	if len(pipelines) == 0 {
		return
	}
	slog.Info("Resuming interrupted pipelines", "count", len(pipelines))
	for _, p := range pipelines {
		go e.Advance(p.PipelineID)
	}
}

// Resume restarts a finished run without redoing work: every step's
// output hash was recorded, so steps before the resume point are reused
// straight from the store while fromStep and everything downstream of it
// run again. With no step named, the steps that did not complete are
// retried.
//
// overrides substitutes a recorded step output with a hash supplied by
// the caller, which is how a run is repaired when a step's output was
// fixed by hand.
func (e *Engine) Resume(pipelineID, fromStep string, overrides map[string]string) (*api.Pipeline, error) {
	if err := e.reopen(pipelineID, fromStep, overrides); err != nil {
		return nil, err
	}
	e.Advance(pipelineID)
	if p, ok := e.cfg.Store.GetPipeline(pipelineID); ok {
		return p, nil
	}
	return nil, fmt.Errorf("pipeline %s vanished while resuming", pipelineID)
}

// reopen applies the overrides, resets the steps to rerun and marks the
// pipeline running again. Separate from Resume so the lock is released
// before advancing, which takes it itself.
func (e *Engine) reopen(pipelineID, fromStep string, overrides map[string]string) error {
	mu := e.lock(pipelineID)
	mu.Lock()
	defer mu.Unlock()

	p, ok := e.cfg.Store.GetPipeline(pipelineID)
	if !ok {
		return fmt.Errorf("no pipeline with ID %s", pipelineID)
	}
	if p.Status == api.StatusRunning {
		return fmt.Errorf("pipeline %s is still running", pipelineID)
	}
	if fromStep != "" && step(p, fromStep) == nil {
		return fmt.Errorf("resume names unknown step %q", fromStep)
	}

	// Validate every override before writing any of them, so a typo
	// cannot leave the run half-repaired.
	for name, hash := range overrides {
		if step(p, name) == nil {
			return fmt.Errorf("override names unknown step %q", name)
		}
		if len(hash) != 64 {
			return fmt.Errorf("override for step %q: invalid hash (wrong length): %s", name, hash)
		}
	}
	for name, hash := range overrides {
		s := step(p, name)
		s.Status = api.StepDone
		s.OutputHash = hash
		s.ErrorMsg = ""
		s.JobID = ""
		e.cfg.Store.UpsertPipelineStep(pipelineID, s)
	}

	rerun := stepsToRerun(p, fromStep)
	// An overridden step is supplied, not computed, so it is not rerun.
	for name := range overrides {
		delete(rerun, name)
	}
	if len(rerun) == 0 {
		return fmt.Errorf("nothing to resume: every step already completed")
	}
	for _, s := range p.Steps {
		if !rerun[s.Name] {
			continue
		}
		s.Status = api.StepPending
		s.JobID = ""
		s.OutputHash = ""
		s.ErrorMsg = ""
		s.StartedUnix = 0
		s.FinishedUnix = 0
		e.cfg.Store.UpsertPipelineStep(pipelineID, s)
	}

	e.cfg.Store.UpdatePipelineStatus(pipelineID, api.StatusRunning, "", 0)
	slog.Info("Pipeline resumed", "pipeline_id", pipelineID, "from_step", fromStep,
		"rerun_steps", len(rerun), "overrides", len(overrides))
	return nil
}

// stepsToRerun returns the steps a resume must run again: the named step
// plus everything transitively downstream of it, or -- with no step
// named -- every step that did not complete, plus their downstream.
//
// Downstream steps are included even when they succeeded, because a
// rerun predecessor may produce a different output hash and their input
// is composed from it. Seeds are never rerun: they are client-supplied,
// not computed.
func stepsToRerun(p *api.Pipeline, fromStep string) map[string]bool {
	dependents := make(map[string][]string, len(p.Steps))
	for _, s := range p.Steps {
		for _, dep := range compose.Dependencies(s.Inputs) {
			dependents[dep.Step] = append(dependents[dep.Step], s.Name)
		}
	}

	rerun := make(map[string]bool)
	var mark func(name string)
	mark = func(name string) {
		s := step(p, name)
		if s == nil || isSeed(s) || rerun[name] {
			return
		}
		rerun[name] = true
		for _, d := range dependents[name] {
			mark(d)
		}
	}

	if fromStep != "" {
		mark(fromStep)
		return rerun
	}
	for _, s := range p.Steps {
		if !isSeed(s) && s.Status != api.StepDone {
			mark(s.Name)
		}
	}
	return rerun
}

// Cancel stops a run: every queued or running step's job is cancelled and
// nothing further is submitted. Steps that already finished keep their
// recorded output, so the run can be resumed from where it stopped.
func (e *Engine) Cancel(pipelineID string) error {
	mu := e.lock(pipelineID)
	mu.Lock()
	defer mu.Unlock()

	p, ok := e.cfg.Store.GetPipeline(pipelineID)
	if !ok {
		return fmt.Errorf("no pipeline with ID %s", pipelineID)
	}
	if p.Status != api.StatusRunning {
		return fmt.Errorf("pipeline %s is already %s", pipelineID, p.Status)
	}

	// Mark the pipeline first: a step's job finishing during cancellation
	// then finds a non-running pipeline and submits nothing further.
	finished := time.Now().Unix()
	e.cfg.Store.UpdatePipelineStatus(pipelineID, api.StatusCancelled, "cancelled by user", finished)

	for _, s := range p.Steps {
		switch s.Status {
		case api.StepRunning:
			if s.JobID != "" {
				if err := e.cfg.CancelJob(s.JobID); err != nil {
					slog.Warn("Could not cancel pipeline step job", "pipeline_id", pipelineID,
						"step", s.Name, "job_id", s.JobID, "error", err)
				}
			}
			s.Status = api.StepFailed
			s.ErrorMsg = "cancelled by user"
		case api.StepPending:
			s.Status = api.StepSkipped
			s.ErrorMsg = "pipeline cancelled before this step ran"
		default:
			continue
		}
		s.FinishedUnix = finished
		e.cfg.Store.UpsertPipelineStep(pipelineID, s)
	}

	slog.Info("Pipeline cancelled", "pipeline_id", pipelineID, "name", p.Name)
	return nil
}
