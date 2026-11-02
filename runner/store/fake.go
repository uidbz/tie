package store

import (
	"sync"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Fake is an in-memory Store for tests of the engine and of code that wires
// into it. It stores the exact same encoded field maps as the tiedb store,
// so both implementations share one definition of what a persisted job or
// step means and cannot drift on encoding.
type Fake struct {
	mu        sync.Mutex
	jobs      map[string]map[string]string
	pipelines map[string]map[string]string
	steps     map[string]map[string]string
}

// NewFake returns an empty in-memory store.
func NewFake() *Fake {
	return &Fake{
		jobs:      map[string]map[string]string{},
		pipelines: map[string]map[string]string{},
		steps:     map[string]map[string]string{},
	}
}

func (f *Fake) Close() error { return nil }

func copyAttrs(a map[string]string) map[string]string {
	c := make(map[string]string, len(a))
	for k, v := range a {
		c[k] = v
	}
	return c
}

func (f *Fake) UpsertJob(job *api.JobStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[job.JobID] = encodeJob(job)
}

func (f *Fake) GetJob(jobID string) (*api.JobStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.jobs[jobID]
	if !ok {
		return nil, false
	}
	return decodeJob(jobID, a), true
}

func (f *Fake) ListJobs(sinceUnix int64, user, status string) ([]*api.JobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*api.JobStatus
	for id, a := range f.jobs {
		if parseInt(a[relCreated]) < sinceUnix {
			continue
		}
		if user != "" && a[relUser] != user {
			continue
		}
		if status != "" && a[relStatus] != status {
			continue
		}
		b := copyAttrs(a)
		b[relLog] = ""
		out = append(out, decodeJob(id, b))
	}
	sortJobsNewestFirst(out)
	return out, nil
}

func (f *Fake) Stats(sinceUnix int64) (*api.JobStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []map[string]string
	for _, a := range f.jobs {
		if parseInt(a[relCreated]) >= sinceUnix {
			rows = append(rows, a)
		}
	}
	return aggregateStats(rows), nil
}

func (f *Fake) ReconcileInterrupted() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, a := range f.jobs {
		if a[relStatus] != api.StatusQueued && a[relStatus] != api.StatusRunning {
			continue
		}
		if a[relExtRef] != "" {
			continue
		}
		a[relStatus] = api.StatusFailed
		a[relErrorMsg] = "Interrupted by runner restart"
		count++
	}
	return count, nil
}

func (f *Fake) ListNonTerminalExternalJobs() ([]*api.JobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*api.JobStatus
	for id, a := range f.jobs {
		if a[relStatus] != api.StatusQueued && a[relStatus] != api.StatusRunning {
			continue
		}
		if a[relExtRef] == "" {
			continue
		}
		out = append(out, decodeJob(id, copyAttrs(a)))
	}
	sortJobsNewestFirst(out)
	return out, nil
}

func (f *Fake) InsertPipeline(p *api.Pipeline) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	pa, steps := encodePipeline(p)
	f.pipelines[p.PipelineID] = pa
	for key, sa := range steps {
		sa[relPipelineID] = p.PipelineID
		f.steps[key] = sa
	}
	return nil
}

func (f *Fake) UpsertPipelineStep(pipelineID string, step *api.PipelineStep) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sa := encodeStep(step)
	sa[relPipelineID] = pipelineID
	f.steps[stepKey(pipelineID, step.Name)] = sa
}

func (f *Fake) UpdatePipelineStatus(pipelineID, status, errorMsg string, finishedUnix int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pa, ok := f.pipelines[pipelineID]
	if !ok {
		pa = map[string]string{}
		f.pipelines[pipelineID] = pa
	}
	setOrClear(pa, relStatus, status)
	setOrClear(pa, relErrorMsg, errorMsg)
	setOrClear(pa, relFinished, padInt(finishedUnix))
}

func setOrClear(a map[string]string, rel, v string) {
	if v == "" {
		delete(a, rel)
		return
	}
	a[rel] = v
}

func (f *Fake) GetPipeline(pipelineID string) (*api.Pipeline, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.pipelines[pipelineID]
	if !ok {
		return nil, false
	}
	p := decodePipeline(a, f.stepAttrsLocked(pipelineID))
	p.PipelineID = pipelineID
	return p, true
}

func (f *Fake) stepAttrsLocked(pipelineID string) []map[string]string {
	var out []map[string]string
	for _, name := range stepNames(f.pipelines[pipelineID]) {
		if a, ok := f.steps[stepKey(pipelineID, name)]; ok {
			out = append(out, a)
		}
	}
	return out
}

func (f *Fake) ListPipelines(sinceUnix int64, user, status string) ([]*api.Pipeline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*api.Pipeline
	for id, a := range f.pipelines {
		if parseInt(a[relCreated]) < sinceUnix {
			continue
		}
		if user != "" && a[relUser] != user {
			continue
		}
		if status != "" && a[relStatus] != status {
			continue
		}
		p := decodePipeline(a, f.stepAttrsLocked(id))
		p.PipelineID = id
		out = append(out, p)
	}
	sortPipelinesNewestFirst(out)
	return out, nil
}

func (f *Fake) ListRunningPipelines() ([]*api.Pipeline, error) {
	return f.ListPipelines(0, "", api.StatusRunning)
}

func (f *Fake) ReapRetention(keepDays int) (int, int, error) {
	if keepDays <= 0 {
		return 0, 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Unix() - int64(keepDays)*86400
	jobs, pipes := 0, 0
	for id, a := range f.jobs {
		if parseInt(a[relCreated]) < cutoff {
			delete(f.jobs, id)
			jobs++
		}
	}
	for id, a := range f.pipelines {
		if parseInt(a[relCreated]) < cutoff {
			for _, name := range stepNames(a) {
				delete(f.steps, stepKey(id, name))
			}
			delete(f.pipelines, id)
			pipes++
		}
	}
	return jobs, pipes, nil
}
