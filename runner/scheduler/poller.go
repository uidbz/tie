package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
)

// StartPoller launches the goroutine that reconciles in-flight
// externally executed jobs against their records. A job counts as
// external by carrying an ext-ref (handle): locally executed jobs never
// get one. The poller also enforces ExternalTimeout and is safe to call
// once per manager.
func (m *Manager) StartPoller() {
	if m.cfg.External == nil {
		return
	}
	interval := m.cfg.PollInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.pollOnce()
			}
		}
	}()
}

// pollOnce describes every in-flight external job once and applies the
// backend-reported state: terminal states finalize the job, non-terminal
// state changes update and persist it, and jobs past ExternalTimeout are
// cancelled through the backend seam and finalized.
func (m *Manager) pollOnce() {
	inflight := map[string]*Job{}
	m.jobs.Range(func(_, v any) bool {
		j := v.(*Job)
		m.mu.Lock()
		ext := !j.Done && j.BatchJobID != ""
		if ext {
			inflight[j.BatchJobID] = j
		}
		m.mu.Unlock()
		return ext
	})
	if len(inflight) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	handles := make([]string, 0, len(inflight))
	for id := range inflight {
		handles = append(handles, id)
	}

	states, err := m.cfg.External.Status(ctx, handles)
	if err != nil {
		slog.Error("External job status poll failed", "error", err)
		return
	}

	now := time.Now()
	for handle, job := range inflight {
		if m.cfg.ExternalTimeout > 0 && !job.created.IsZero() &&
			now.Sub(job.created) > m.cfg.ExternalTimeout {
			if err := m.cfg.External.Cancel(ctx, handle, "job timeout"); err != nil {
				slog.Error("Failed to terminate timed-out external job",
					"job_id", job.JobID, "handle", handle, "error", err)
			}
			m.mu.Lock()
			job.ErrorMsg = executor.AddLine(job.ErrorMsg, "job exceeded configured timeout")
			job.exitCode = 1
			m.mu.Unlock()
			m.finalize(job)
			continue
		}

		state, ok := states[handle]
		if !ok {
			continue
		}

		switch state.Status {
		case api.StatusDone:
			m.mu.Lock()
			job.exitCode = state.ExitCode
			m.mu.Unlock()
			m.finalize(job)
		case api.StatusFailed:
			m.mu.Lock()
			job.exitCode = state.ExitCode
			if state.Reason != "" {
				job.ErrorMsg = executor.AddLine(job.ErrorMsg, state.Reason)
			} else if job.ErrorMsg == "" {
				job.ErrorMsg = executor.AddLine(job.ErrorMsg, "external job failed")
			}
			m.mu.Unlock()
			m.finalize(job)
		default:
			m.mu.Lock()
			changed := job.Status != state.Status
			if changed {
				job.Status = state.Status
				if state.Status == api.StatusRunning && job.started.IsZero() {
					job.started = now
					job.StartedUnix = now.Unix()
					job.IsRunning = true
				}
			}
			snap := job.copyLocked()
			m.mu.Unlock()
			if changed {
				m.cfg.Store.UpsertJob(snap)
			}
		}
	}
}
