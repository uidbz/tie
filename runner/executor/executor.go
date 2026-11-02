package executor

import (
	"context"
	"encoding/json"

	"github.com/uidbz/tie/runner/api"
)

// Local runs jobs on this machine. Run blocks for the whole execution,
// updating job (log, percent, result, error, memory) as the container
// reports, and returns the exit state. It must honor ctx: cancelling
// ctx aborts the execution even before the container exists, which is
// how the scheduler cancels a job whose launch has not completed. Run
// never finalizes the job — the caller owns the terminal transition
// (status, exit reason, persistence) because that is where the store and
// pipeline machinery live.
type Local interface {
	Run(ctx context.Context, job *api.JobStatus, opts RunOptions) ExitInfo
	// Stop asks a running container to terminate; Run then returns. Used
	// for user cancellation.
	Stop(containerName string) error
}

// RunOptions carry what the runtime needs beyond the wire-visible job
// record: the resource sizing to enforce, the settings document to hand
// to the container, and lifecycle hooks (the runner uses these to
// register the container as a live task while it runs).
type RunOptions struct {
	Spec     Spec
	Settings json.RawMessage
	Hooks    Hooks
}

// Hooks are optional observation points around the container's lifetime,
// plus the sink for live updates to the job record. A runtime that
// scans output or samples memory on background goroutines MUST route
// every job-record mutation through the Log/Err/Percent/Result/Mem
// sinks: the runner's implementations serialize them against the
// status endpoints reading the same record. Sinks nil means direct
// mutation (single-threaded callers: tests, standalone use).
type Hooks struct {
	OnStart func(containerName string)
	OnEnd   func(containerName string)

	Log     func(line string)
	Err     func(line string)
	Percent func(pct float32)
	Result  func(hash string)
	Mem     func(curMB int64)
}

// log/err/percent/result/mem dispatch to a sink or fall back to direct
// mutation of the record.
func (h Hooks) log(job *api.JobStatus, line string) {
	if h.Log != nil {
		h.Log(line)
		return
	}
	job.Log = AddLine(job.Log, line)
}

func (h Hooks) err(job *api.JobStatus, line string) {
	if h.Err != nil {
		h.Err(line)
		return
	}
	job.ErrorMsg = AddLine(job.ErrorMsg, line)
}

func (h Hooks) percent(job *api.JobStatus, pct float32) {
	if h.Percent != nil {
		h.Percent(pct)
		return
	}
	job.Percent = pct
}

func (h Hooks) result(job *api.JobStatus, hash string) {
	if h.Result != nil {
		h.Result(hash)
		return
	}
	job.OutputHash = hash
}

func (h Hooks) mem(job *api.JobStatus, curMB int64) {
	if h.Mem != nil {
		h.Mem(curMB)
		return
	}
	job.MemCurrentMB = curMB
	if curMB > job.MemPeakMB {
		job.MemPeakMB = curMB
	}
}

// ExitInfo is how a local execution ended: the container's exit code and
// whether the kernel OOM-killed it (from docker inspect, read before
// --rm removes the container record).
type ExitInfo struct {
	ExitCode  int
	OOMKilled bool
}

// External is the seam for a deployment's remote execution backend (a
// hosted batch service, a cluster scheduler): submit returns a handle the
// runner persists as the job's ext-ref, Status maps handles to states for
// the poller, Cancel terminates by handle. The core never implements an
// external backend; deployments inject one.
//
// Jobs executed externally bypass the local queue. The runner's poller
// reconciles in-flight handles: mapped non-terminal states update the
// job record, terminal states finalize it, and a configured timeout
// cancels the handle through this same interface.
type External interface {
	Submit(ctx context.Context, job *api.JobStatus) (handle string, err error)
	Status(ctx context.Context, handles []string) (map[string]HandleState, error)
	Cancel(ctx context.Context, handle, reason string) error
}

// HandleState is the poller's view of one externally executed job.
// Status uses the frozen job status vocabulary (queued/running/done/
// failed); non-terminal backend states must collapse onto queued/running.
type HandleState struct {
	Status   string
	ExitCode int
	Reason   string
}
