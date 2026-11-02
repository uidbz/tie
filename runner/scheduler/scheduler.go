// Package scheduler owns the live job set and admits queued jobs to the
// local executor against the runner's resource pool. It sits between the
// API surface (which submits and queries jobs) and the executor (which
// runs them), and it is the one place a job's terminal state is decided:
// every completion path — local container exit, external backend poll,
// timeout, cancel — funnels through finalize, which stamps the terminal
// status, persists it, and fires the OnFinished hook (the pipeline
// sequencer's advance trigger).
//
// Scheduling is scan-and-fit against a total resource pool: a job starts
// when its type's executor.Spec fits free capacity and no job of the same
// handler is running (jobs of one handler share the container's scratch
// mount, so they serialize). Jobs submitted to an external backend bypass
// the queue entirely: SubmitExternal hands them to the deployment's
// executor.External, and StartPoller tracks them until terminal.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/store"
)

// Job is a live job: the wire record plus the scheduler's private
// bookkeeping (settings, process exit state, in-memory retention).
// The embedded record is what the API reports and the store persists.
type Job struct {
	*api.JobStatus

	// Settings is handed to the container as /settings.json; it is
	// deliberately never part of the wire record or the store.
	Settings json.RawMessage

	created  time.Time
	started  time.Time
	finished time.Time

	cancelled  bool
	exitCode   int
	oomKilled  bool
	deletionAt time.Time // in-memory retention: after this the live record goes away
}

// Config wires the scheduler to its collaborators.
type Config struct {
	// Local executes queued jobs on this machine. Required.
	Local executor.Local
	// External is the optional remote-backend seam; nil disables
	// external submission and polling.
	External executor.External
	// Store is the durable history. Required.
	Store store.Store
	// TotalCPU/TotalRAMMB/TotalGPU size the admission pool.
	TotalCPU, TotalRAMMB, TotalGPU int
	// ResolveSpec maps a job type to its resource sizing. nil falls back
	// to executor.SpecFor with no overrides. It is consulted per job, so
	// a hot-reloaded configuration takes effect immediately.
	ResolveSpec func(jobType string) (executor.Spec, bool)
	// Filehost is recorded on each job (wire-visible provenance).
	Filehost string
	// OnFinished fires when a job reaches a terminal state (finalize) and
	// when a queued job is cancelled without ever running. The pipeline
	// sequencer uses it to advance the step graph.
	OnFinished func(job *api.JobStatus)
	// OnStart/OnEnd observe job execution lifetimes (the runner registers
	// live tasks through them). They fire around the local execution of a
	// job with the live job record.
	OnStart func(job *api.JobStatus)
	OnEnd   func(job *api.JobStatus)
	// ExternalTimeout bounds how long an externally executed job may stay
	// non-terminal; 0 disables the check.
	ExternalTimeout time.Duration
	// PollInterval is how often the external poller reconciles; 0 uses
	// 10s.
	PollInterval time.Duration
}

// Manager is the scheduler proper: the live job map, the queue, the
// resource pool, and the finalize path.
type Manager struct {
	cfg Config

	jobs  sync.Map // jobID -> *Job, the live set including external jobs
	queue *Queue
	mu    sync.Mutex

	running      map[string]*Job // jobID -> admitted and running locally
	busyHandlers map[string]bool // handler -> a job of it is running
	// jobCancels unblocks a local execution whose container has not
	// finished starting: docker stop cannot reach a container that does
	// not exist yet, but cancelling the launch context can.
	jobCancels map[string]context.CancelFunc

	usedCPU   int
	usedRAMMB int
	usedGPU   int

	wake chan struct{} // buffered(1); pokes the admission loop

	stopOnce sync.Once
	stop     chan struct{}
}

// New builds a manager and starts the admission loop.
func New(cfg Config) *Manager {
	m := &Manager{
		cfg:          cfg,
		queue:        NewQueue(),
		running:      make(map[string]*Job),
		busyHandlers: make(map[string]bool),
		jobCancels:   make(map[string]context.CancelFunc),
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
	go func() {
		for {
			select {
			case <-m.stop:
				return
			case <-m.wake:
				m.admit()
			}
		}
	}()
	return m
}

// Stop ends the admission and poller loops.
func (m *Manager) Stop() { m.stopOnce.Do(func() { close(m.stop) }) }

func (m *Manager) specFor(jobType string) (executor.Spec, bool) {
	if m.cfg.ResolveSpec != nil {
		return m.cfg.ResolveSpec(jobType)
	}
	return executor.SpecFor(jobType, nil)
}

// SpecFor exposes the job-type sizing table (dispatch and validation
// consult it outside the scheduler).
func (m *Manager) SpecFor(jobType string) (executor.Spec, bool) { return m.specFor(jobType) }

// poke asks the scheduler to run an admit pass. Non-blocking: a pending
// wake already covers the request.
func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// admit launches every queued job that currently fits free capacity and
// whose handler isn't already running. Scan-and-fit (not stop-at-first):
// a job that can't fit is skipped so unrelated jobs behind it still
// start. A large job can therefore be starved by a stream of small ones
// — acceptable for this runner.
func (m *Manager) admit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.queue.List() {
		if spec, ok := m.tryAdmitLocked(job); ok {
			m.queue.Remove(job.JobID)
			go m.runJob(job, spec)
		}
	}
}

// tryAdmitLocked reserves resources for job and records it as running if
// it can start right now — its handler is idle and its spec fits free
// capacity. Returns the reserved spec and true on success.
func (m *Manager) tryAdmitLocked(job *Job) (executor.Spec, bool) {
	spec, ok := m.specFor(job.JobType)
	if !ok {
		return executor.Spec{}, false // not locally schedulable; submission rejects first
	}
	if m.busyHandlers[job.Handler] {
		return executor.Spec{}, false // one job per handler at a time (shared scratch mount)
	}
	if !m.fits(spec) {
		return executor.Spec{}, false
	}
	m.usedCPU += spec.CPU
	m.usedRAMMB += spec.RAMMB
	m.usedGPU += spec.GPU
	m.busyHandlers[job.Handler] = true
	m.running[job.JobID] = job
	return spec, true
}

// runJob marks the job running, executes it, stamps the exit state,
// finalizes, then releases its reservation and re-admits.
//
// Concurrency contract: the live JobStatus record is shared with HTTP
// handlers (status/running endpoints marshal it) and with the runtime's
// scanner/sampler goroutines. Every write to it goes through m.mu, and
// anything handed to the store or to hooks is a snapshot copy; the
// runtime performs its own writes only through the hooks sinks.
func (m *Manager) runJob(job *Job, spec executor.Spec) {
	m.mu.Lock()
	job.IsRunning = true
	job.Status = api.StatusRunning
	job.started = time.Now()
	job.StartedUnix = job.started.Unix()
	snap := job.copyLocked()
	m.mu.Unlock()
	m.cfg.Store.UpsertJob(snap)

	if m.cfg.OnStart != nil {
		m.cfg.OnStart(job.JobStatus)
	}
	// The launch context makes a cancel reachable before the container
	// exists: docker stop cannot name a container that was never created,
	// but cancelling this context unblocks the starting command.
	runCtx, cancelRun := context.WithCancel(context.Background())
	m.mu.Lock()
	m.jobCancels[job.JobID] = cancelRun
	m.mu.Unlock()
	info := m.cfg.Local.Run(runCtx, job.JobStatus, executor.RunOptions{
		Spec:     spec,
		Settings: job.Settings,
		Hooks:    m.liveHooks(job),
	})
	cancelRun()
	m.mu.Lock()
	delete(m.jobCancels, job.JobID)
	m.mu.Unlock()
	if m.cfg.OnEnd != nil {
		m.cfg.OnEnd(job.JobStatus)
	}
	m.mu.Lock()
	job.exitCode, job.oomKilled = info.ExitCode, info.OOMKilled
	m.mu.Unlock()

	m.finalize(job)

	m.mu.Lock()
	m.usedCPU -= spec.CPU
	m.usedRAMMB -= spec.RAMMB
	m.usedGPU -= spec.GPU
	delete(m.busyHandlers, job.Handler)
	delete(m.running, job.JobID)
	m.mu.Unlock()
	m.poke()
}

// liveHooks is the sink pair for one running job: the runtime routes
// every mutation the scanner/sampler goroutines would make through
// here, so those writes serialize against the status endpoints (see the
// runJob concurrency contract).
func (m *Manager) liveHooks(job *Job) executor.Hooks {
	return executor.Hooks{
		Log: func(line string) {
			m.mu.Lock()
			job.Log = executor.AddLine(job.Log, line)
			m.mu.Unlock()
		},
		Err: func(line string) {
			m.mu.Lock()
			job.ErrorMsg = executor.AddLine(job.ErrorMsg, line)
			m.mu.Unlock()
		},
		Percent: func(pct float32) {
			m.mu.Lock()
			job.Percent = pct
			m.mu.Unlock()
		},
		Result: func(hash string) {
			m.mu.Lock()
			job.OutputHash = hash
			m.mu.Unlock()
		},
		Mem: func(curMB int64) {
			m.mu.Lock()
			job.MemCurrentMB = curMB
			if curMB > job.MemPeakMB {
				job.MemPeakMB = curMB
			}
			m.mu.Unlock()
		},
	}
}

// finalize records the terminal state of a job exactly once. Every exit
// path (container completion with or without early setup errors, external
// poll, timeout) passes through here, so a job can never end unrecorded.
func (m *Manager) finalize(job *Job) {
	// cancelled and ErrorMsg may be set concurrently by Cancel; snapshot
	// them under the scheduler lock before deciding the outcome.
	m.mu.Lock()
	cancelled := job.cancelled
	errmsg := job.ErrorMsg

	job.IsRunning = false
	job.Done = true
	job.finished = time.Now()
	job.FinishedUnix = job.finished.Unix()
	job.RunningTime = job.finished.Sub(job.created).String()
	job.deletionAt = job.finished.Add(7 * 24 * time.Hour) // keep the live record for a week
	switch {
	case cancelled:
		job.Status = api.StatusCancelled
	case errmsg != "":
		job.Status = api.StatusFailed
	default:
		job.Status = api.StatusDone
	}
	switch {
	case job.oomKilled || job.exitCode == 137:
		job.ExitReason = "out-of-memory"
	case cancelled:
		job.ExitReason = "cancelled"
	case errmsg != "":
		job.ExitReason = "error"
	default:
		job.ExitReason = "ok"
	}
	snap := job.copyLocked()
	m.mu.Unlock()

	slog.Info("Job completed",
		"handler", snap.Handler,
		"input_hash", snap.InputHash,
		"output_hash", snap.OutputHash,
		"running_time", snap.RunningTime,
		"status", snap.Status,
		"has_error", errmsg != "",
	)
	m.cfg.Store.UpsertJob(snap)
	// Every terminal transition passes through here, so this is the one
	// hook a pipeline needs to learn a step finished. It must follow the
	// UpsertJob above: advancing reads the job's outcome back from the
	// store. The snapshot keeps the live record free to change.
	if m.cfg.OnFinished != nil {
		m.cfg.OnFinished(snap)
	}
}

// fits reports whether spec's resources are free right now. Caller holds mu.
func (m *Manager) fits(spec executor.Spec) bool {
	return m.usedCPU+spec.CPU <= m.cfg.TotalCPU &&
		m.usedRAMMB+spec.RAMMB <= m.cfg.TotalRAMMB &&
		m.usedGPU+spec.GPU <= m.cfg.TotalGPU
}

// CanFit reports whether a job of this spec could start immediately.
func (m *Manager) CanFit(spec executor.Spec) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fits(spec)
}

// SetCapacity replaces the resource pool totals (config reload). It
// refuses totals below what running jobs already reserved — accepting
// them would make the accounting go negative and admit jobs the machine
// can't actually run. It returns false (keeping the old totals) in that
// case; the caller retries after the running jobs finish.
func (m *Manager) SetCapacity(cpu, ramMB, gpu int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cpu < m.usedCPU || ramMB < m.usedRAMMB || gpu < m.usedGPU {
		return false
	}
	m.cfg.TotalCPU, m.cfg.TotalRAMMB, m.cfg.TotalGPU = cpu, ramMB, gpu
	return true
}

// WithinCapacity reports whether spec could ever fit the total pool. A
// spec that exceeds the totals in any dimension can never be admitted.
func (m *Manager) WithinCapacity(spec executor.Spec) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return spec.CPU <= m.cfg.TotalCPU && spec.RAMMB <= m.cfg.TotalRAMMB && spec.GPU <= m.cfg.TotalGPU
}

// Snapshot is a point-in-time view of the resource pool, reporting the
// live Running/Queued counts the stats endpoints overlay on stored
// history.
type Snapshot struct {
	TotalCPU, TotalRAMMB, TotalGPU int
	FreeCPU, FreeRAMMB, FreeGPU    int
	Running                        int
	QueueLength                    int
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{
		TotalCPU:    m.cfg.TotalCPU,
		TotalRAMMB:  m.cfg.TotalRAMMB,
		TotalGPU:    m.cfg.TotalGPU,
		FreeCPU:     m.cfg.TotalCPU - m.usedCPU,
		FreeRAMMB:   m.cfg.TotalRAMMB - m.usedRAMMB,
		FreeGPU:     m.cfg.TotalGPU - m.usedGPU,
		Running:     len(m.running),
		QueueLength: m.queue.Size(),
	}
}

// RunningJobs returns the jobs currently executing.
func (m *Manager) RunningJobs() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.running))
	for _, j := range m.running {
		out = append(out, j)
	}
	return out
}

// NewJob creates a queued job record from a submission and persists the
// initial state. It does not queue anything: the caller decides between
// Submit (local) and SubmitExternal (remote backend).
func (m *Manager) NewJob(req api.JobRequest, isProd bool, deployManager string) *Job {
	now := time.Now()
	job := &Job{
		JobStatus: &api.JobStatus{
			DeployManager: deployManager,
			JobID:         uuid.NewString(),
			InputHash:     req.InputHash,
			AppHash:       req.AppHash,
			JobType:       req.JobType,
			Handler:       req.Handler,
			IsDebug:       req.Debug,
			CreatedUnix:   now.Unix(),
			IsProd:        isProd,
			User:          req.User,
			Filehost:      m.cfg.Filehost,
			Status:        api.StatusQueued,
			ReqCPU:        req.ReqCPU,
			ReqRAMMB:      req.ReqRAMMB,
			PipelineID:    req.PipelineID,
			StepName:      req.StepName,
		},
		Settings: req.Settings,
		created:  now,
	}
	// Sizing is driven by the job type, not the client request: record
	// the resolved spec so history and stats reflect what the job gets.
	if spec, ok := m.specFor(req.JobType); ok {
		job.ReqCPU = spec.CPU
		job.ReqRAMMB = spec.RAMMB
	}
	m.jobs.Store(job.JobID, job)
	m.cfg.Store.UpsertJob(job.JobStatus)
	slog.Info("Job created", "job_id", job.JobID, "handler", job.Handler, "job_type", job.JobType, "is_prod", isProd, "user", job.User)
	return job
}

// Submit queues a local job for admission.
func (m *Manager) Submit(job *Job) {
	m.queue.Push(job)
	slog.Info("Job submitted to queue", "job_id", job.JobID, "handler", job.Handler, "queue_size", m.queue.Size())
	m.poke()
}

// ErrNoExternalBackend is returned by SubmitExternal when no external
// backend is configured.
var ErrNoExternalBackend = errors.New("external execution backend not enabled on this runner")

// HasExternal reports whether an external execution backend is wired.
func (m *Manager) HasExternal() bool { return m.cfg.External != nil }

// QueueList snapshots the waiting queue for the queue endpoint.
func (m *Manager) QueueList() []*api.JobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.queue.List()
	out := make([]*api.JobStatus, 0, len(items))
	for _, j := range items {
		out = append(out, j.copyLocked())
	}
	return out
}

// copyLocked snapshots the wire record. Callers must hold m.mu: that is
// what keeps HTTP marshalling from interleaving with the executor's
// writes to the live record. (The historic code marshalled live
// pointers; the race was always there, just rarely armed.)
func (j *Job) copyLocked() *api.JobStatus {
	c := *j.JobStatus
	return &c
}

// LiveCopy snapshots the live record for a job id (status endpoints).
func (m *Manager) LiveCopy(jobID string) (*api.JobStatus, bool) {
	if v, ok := m.jobs.Load(jobID); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		return v.(*Job).copyLocked(), true
	}
	return nil, false
}

// RunningStatuses snapshots the wire records of the currently running
// jobs (the running/active endpoints).
func (m *Manager) RunningStatuses() []*api.JobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*api.JobStatus, 0, len(m.running))
	for _, j := range m.running {
		out = append(out, j.copyLocked())
	}
	return out
}

// SubmitExternal hands a job to the deployment's external backend. It
// bypasses the local queue; the poller tracks it to completion. The
// backend fills the job's ext-queue/ext-region provenance fields as part
// of Submit; the returned handle becomes the job's ext-ref.
func (m *Manager) SubmitExternal(ctx context.Context, job *Job) error {
	if m.cfg.External == nil {
		return ErrNoExternalBackend
	}
	// The backend stamps provenance fields onto the record it is given;
	// hand it a snapshot and fold the result back under the lock, so a
	// concurrent status read never sees a torn record.
	m.mu.Lock()
	backendView := job.copyLocked()
	m.mu.Unlock()
	handle, err := m.cfg.External.Submit(ctx, backendView)
	if err != nil {
		m.mu.Lock()
		job.Queue, job.Region = backendView.Queue, backendView.Region
		job.ErrorMsg = executor.AddLine(job.ErrorMsg, "external submit failed: "+err.Error())
		job.exitCode = 1
		m.mu.Unlock()
		// The job already reached a terminal state (the backend refused
		// it), so it must finalize: store write plus the OnFinished hook,
		// or a pipeline step waiting on it would hang forever. finalize
		// takes the lock itself, so call it outside.
		m.finalize(job)
		slog.Error("Failed to submit external job", "job_id", job.JobID, "job_type", job.JobType, "error", err)
		return err
	}
	m.mu.Lock()
	job.BatchJobID = handle
	job.Status = api.StatusQueued
	job.Queue, job.Region = backendView.Queue, backendView.Region
	snap := job.copyLocked()
	m.mu.Unlock()
	m.cfg.Store.UpsertJob(snap)
	slog.Info("Submitted external job", "job_id", job.JobID, "job_type", job.JobType, "handle", handle, "queue", job.Queue)
	return nil
}

// Cancel cancels a job by ID. A still-queued job is removed from the
// queue and marked cancelled. An actively running local job is killed by
// stopping its container; the completion path then finalizes it as
// cancelled. An externally executed job is cancelled through the backend
// seam; its poller finalizes. Returns an error if no live job matches.
func (m *Manager) Cancel(jobID string) error {
	if v, ok := m.jobs.Load(jobID); ok {
		job := v.(*Job)
		if job.BatchJobID != "" && !job.Done {
			if m.cfg.External != nil {
				if err := m.cfg.External.Cancel(context.Background(), job.BatchJobID, "cancelled by user"); err != nil {
					return err
				}
			}
			m.mu.Lock()
			job.cancelled = true
			job.ErrorMsg = executor.AddLine(job.ErrorMsg, "cancelled by user")
			snap := job.copyLocked()
			m.mu.Unlock()
			m.cfg.Store.UpsertJob(snap)
			slog.Info("Cancelled external job", "job_id", jobID, "handle", job.BatchJobID)
			return nil
		}
	}

	if m.queue.Remove(jobID) {
		if v, ok := m.jobs.Load(jobID); ok {
			job := v.(*Job)
			m.mu.Lock()
			job.cancelled = true
			job.Status = api.StatusCancelled
			job.ErrorMsg = executor.AddLine(job.ErrorMsg, "cancelled by user")
			snap := job.copyLocked()
			m.mu.Unlock()
			m.cfg.Store.UpsertJob(snap)
			// A queued job never runs, so it never reaches finalize; tell
			// the pipeline hook directly or the step would stay running
			// forever.
			if m.cfg.OnFinished != nil {
				m.cfg.OnFinished(snap)
			}
		}
		slog.Info("Cancelled queued job", "job_id", jobID)
		return nil
	}

	m.mu.Lock()
	cur := m.running[jobID]
	cancelRun := m.jobCancels[jobID]
	if cur != nil {
		cur.cancelled = true
	}
	m.mu.Unlock()
	if cur != nil {
		// Per-handler serialization guarantees at most one running
		// container per handler, so the handler-derived container name is
		// unambiguous.
		containerName := executor.ContainerName(cur.Handler)
		slog.Info("Cancelling running job", "job_id", jobID, "container", containerName)
		if err := m.cfg.Local.Stop(containerName); err != nil {
			// The container may not exist yet (the run command is still
			// launching). Unblocking the launch is the reliable cancel
			// path there; the local runtime is required to honor ctx, so
			// Run returns and finalizes as cancelled either way. Retry
			// the stop best-effort in case the container came up between
			// the first attempt and the launch cancel.
			if cancelRun == nil {
				return fmt.Errorf("failed to stop running job container: %w", err)
			}
			cancelRun()
			if err2 := m.cfg.Local.Stop(containerName); err2 != nil {
				slog.Info("Container stop after launch cancel found no container; launch aborted",
					"job_id", jobID, "container", containerName)
			}
		}
		return nil
	}

	return fmt.Errorf("no queued or running job with ID %s", jobID)
}

// CleanUp drops live job records past their in-memory retention (a week
// after finishing). The durable store keeps history independently; this
// only trims the hot map.
func (m *Manager) CleanUp() {
	deleted := 0
	now := time.Now()
	m.jobs.Range(func(key, value any) bool {
		job := value.(*Job)
		if !job.deletionAt.IsZero() && now.After(job.deletionAt) {
			m.jobs.Delete(key)
			deleted++
		}
		return true
	})
	if deleted > 0 {
		slog.Info("Job cleanup completed", "deleted_jobs", deleted)
	}
}

// Live returns the live record for a job id. Status endpoints read live
// first and fall back to the store.
func (m *Manager) Live(jobID string) (*Job, bool) {
	v, ok := m.jobs.Load(jobID)
	if !ok {
		return nil, false
	}
	return v.(*Job), true
}

// PutLive re-registers externally executed jobs loaded from the store at
// startup (RestoreExternal), so the poller resumes tracking jobs that
// outlived a runner restart. It never touches terminal jobs.
func (m *Manager) PutLive(job *api.JobStatus) {
	m.jobs.Store(job.JobID, &Job{JobStatus: job, created: unixOrZero(job.CreatedUnix)})
}

// RestoreExternal reloads non-terminal externally executed jobs from the
// store into the live map. It returns how many were resumed.
func (m *Manager) RestoreExternal() (int, error) {
	jobs, err := m.cfg.Store.ListNonTerminalExternalJobs()
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		m.PutLive(j)
	}
	if len(jobs) > 0 {
		slog.Info("Resumed tracking of in-flight external jobs", "count", len(jobs))
	}
	return len(jobs), nil
}

func unixOrZero(u int64) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0)
}
