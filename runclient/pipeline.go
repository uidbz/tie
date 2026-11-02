package runclient

// Pipelines are submitted whole and sequenced by the runner, so unlike
// a single job the client sends a graph and then has nothing left to do:
// it may exit immediately. Waiting is a convenience layered on top.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Step returns the named step of a pipeline, or nil.
func Step(p *api.Pipeline, name string) *api.PipelineStep {
	for _, s := range p.Steps {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// EntryHandler is the image of the lowest-ordinal step that runs one,
// skipping seeds. Cancelling and resuming are authorized against this
// image's DeployKey, so this is what to look the key up by.
func EntryHandler(p *api.Pipeline) string {
	var entry *api.PipelineStep
	for _, s := range p.Steps {
		if s.Handler == "" {
			continue // a seed
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

// PipelineDone reports whether the run will not change state again.
func PipelineDone(p *api.Pipeline) bool {
	switch p.Status {
	case api.StatusDone, api.StatusFailed, api.StatusCancelled:
		return true
	}
	return false
}

// SubmitPipeline posts a graph and returns the accepted pipeline. It
// returns as soon as the runner has validated and stored the graph; the
// run continues without the client. Submitting only enqueues a graph, so
// the timeout covers validation rather than the run itself.
func SubmitPipeline(runner string, req api.PipelineRequest) (api.Pipeline, error) {
	var p api.Pipeline
	if req.User == "" {
		req.User = CurrentUser()
	}
	if err := do(call{Method: http.MethodPost, Runner: runner, Path: "/api/pipelines", Body: req, Timeout: 60 * time.Second}, &p); err != nil {
		return p, fmt.Errorf("submitting pipeline: %w", err)
	}
	return p, nil
}

// GetPipeline reads the current state of one pipeline.
func GetPipeline(runner, pipelineID string) (api.Pipeline, error) {
	var p api.Pipeline
	if err := do(call{Method: http.MethodGet, Runner: runner, Path: "/api/pipelines/" + seg(pipelineID)}, &p); err != nil {
		return p, fmt.Errorf("getting pipeline: %w", err)
	}
	return p, nil
}

// ListPipelines returns pipelines from one runner, optionally filtered.
func ListPipelines(runner string, since time.Duration, user, status string) ([]api.Pipeline, error) {
	var out []api.Pipeline
	if err := do(call{Method: http.MethodGet, Runner: runner, Path: "/api/pipelines", Query: listQuery(since, user, status)}, &out); err != nil {
		return nil, fmt.Errorf("listing pipelines: %w", err)
	}
	return out, nil
}

// CancelPipeline stops a running pipeline. Steps already submitted are
// cancelled with it; steps that never ran are recorded as skipped.
// deployKey must be the key of the image EntryHandler names, since
// cancelling here cancels those steps' jobs.
func CancelPipeline(runner, pipelineID, deployKey string) error {
	err := do(call{Method: http.MethodDelete, Runner: runner, Path: "/api/pipelines/" + seg(pipelineID), Key: deployKey}, nil)
	if errors.Is(err, ErrUnauthorized) {
		return fmt.Errorf("%w for this pipeline's entry step", ErrUnauthorized)
	}
	return err
}

// ResumePipeline re-runs the part of a finished pipeline that did not
// complete. With fromStep set, that step and everything downstream
// re-runs even if it succeeded. overrides supplies a step's output from
// a hash instead of running it. Resuming submits jobs, so it takes the
// same key as CancelPipeline.
func ResumePipeline(runner, pipelineID, deployKey, fromStep string, overrides map[string]string) (api.Pipeline, error) {
	var p api.Pipeline
	body := map[string]any{}
	if fromStep != "" {
		body["From"] = fromStep
	}
	if len(overrides) > 0 {
		body["With"] = overrides
	}
	err := do(call{Method: http.MethodPost, Runner: runner, Path: "/api/pipelines/" + seg(pipelineID) + "/resume", Key: deployKey, Body: body, Timeout: 60 * time.Second}, &p)
	if errors.Is(err, ErrUnauthorized) {
		return p, fmt.Errorf("%w for this pipeline's entry step", ErrUnauthorized)
	}
	if err != nil {
		return p, fmt.Errorf("resuming pipeline: %w", err)
	}
	return p, nil
}

// WaitPipeline polls until the run reaches a terminal state, reporting
// each step transition through onStep. Waiting is optional by design —
// the run does not depend on it — so this is for scripts that want to
// use the output afterwards.
func WaitPipeline(runner, pipelineID string, poll time.Duration, onStep func(step *api.PipelineStep)) (api.Pipeline, error) {
	return WaitPipelineCtx(context.Background(), runner, pipelineID, poll, onStep)
}

// WaitPipelineCtx is WaitPipeline with a context threaded through the
// wait: transient status errors are tolerated with the same exponential
// backoff as the job watch, and a cancelled context stops the wait
// promptly with a benign nil return — the pipeline keeps running on the
// runner, exactly like a cancelled job watch.
func WaitPipelineCtx(ctx context.Context, runner, pipelineID string, poll time.Duration, onStep func(step *api.PipelineStep)) (api.Pipeline, error) {
	if poll <= 0 {
		poll = 3 * time.Second
	}
	seen := make(map[string]string)
	var backoff pollBackoff
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("watching cancelled; pipeline %s still running on %s — resume with tie-run pipeline show %s\n", pipelineID, runner, pipelineID)
			return api.Pipeline{}, nil
		default:
		}
		p, err := GetPipeline(runner, pipelineID)
		if err != nil {
			stop, ctxErr := backoff.failure(ctx)
			if ctxErr != nil {
				fmt.Printf("watching cancelled; pipeline %s still running on %s — resume with tie-run pipeline show %s\n", pipelineID, runner, pipelineID)
				return p, nil
			}
			if stop {
				return p, fmt.Errorf("pipeline %s is still on %s; check with tie-run pipeline show %s", pipelineID, runner, pipelineID)
			}
			continue
		}
		backoff.success()
		if onStep != nil {
			for _, s := range p.Steps {
				if seen[s.Name] != s.Status {
					seen[s.Name] = s.Status
					onStep(s)
				}
			}
		}
		if PipelineDone(&p) {
			if p.Status == api.StatusFailed {
				msg := p.ErrorMsg
				if msg == "" {
					msg = "pipeline failed"
				}
				return p, errors.New(msg)
			}
			return p, nil
		}
		if !sleepCtx(ctx, poll) {
			fmt.Printf("watching cancelled; pipeline %s still running on %s — resume with tie-run pipeline show %s\n", pipelineID, runner, pipelineID)
			return p, nil
		}
	}
}
