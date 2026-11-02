package executor

import (
	"context"
	"encoding/json"

	"github.com/uidbz/tie/runner/api"
)

// Local runs jobs on this machine. Run blocks for the whole execution,
// updating job (log, percent, result, error, memory) as the container
// reports, and returns the exit state. It never finalizes the job — the
// caller owns the terminal transition (status, exit reason, persistence)
// because that is where the store and pipeline machinery live.
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

// Hooks are optional observation points around the container's lifetime.
// OnStart fires once the container name is reserved and the command has
// started; OnEnd fires when the command has exited.
type Hooks struct {
	OnStart func(containerName string)
	OnEnd   func(containerName string)
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
