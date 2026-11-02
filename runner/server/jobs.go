package server

// Submission and read/cancel for single jobs. Where a job runs (here vs
// a backend) is the dispatch package's decision; this file turns that
// decision into HTTP.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/sec"
)

// errExternalDisabled is reported for external-backend job types on a
// runner without one (historic 503).
var errExternalDisabled = errors.New("external execution backend not enabled on this runner")

func (s *Server) extCapable(jobType string) bool {
	return s.d.ExtCapable != nil && s.d.ExtCapable(jobType)
}

// validateJobRequest rejects a request this runner can never execute: an
// external-backend job type on a runner without that backend, or a local
// job type that is unknown or larger than the whole resource pool.
// Pipelines validate every step through here at submission time, so an
// unrunnable step is reported to the client rather than discovered
// halfway through a run.
func (s *Server) validateJobRequest(req api.JobRequest) error {
	if s.extCapable(req.JobType) {
		if !s.d.Sched.HasExternal() {
			return errExternalDisabled
		}
		return nil
	}
	spec, ok := s.d.Sched.SpecFor(req.JobType)
	if !ok {
		//lint:ignore ST1005 historic wire string, preserved byte-for-byte
		return fmt.Errorf("Unknown job type: %s", req.JobType)
	}
	if !s.d.Sched.WithinCapacity(spec) {
		//lint:ignore ST1005 historic wire string, preserved byte-for-byte
		return fmt.Errorf("Job type %s (%d cores / %d MB / %d GPU) exceeds runner capacity",
			req.JobType, spec.CPU, spec.RAMMB, spec.GPU)
	}
	// An image that was never pushed to the registry used to fail ~0.3s
	// after submit as 'cannot pull image: manifest unknown'. Check the
	// manifest at submit so the user gets the actionable message
	// immediately. The check fails open: a broken check must never block
	// real jobs.
	if s.d.ImageCheck != nil {
		if err := s.d.ImageCheck(req.Handler); err != nil {
			return err
		}
	}
	return nil
}

// submitJob creates a job and starts it on the right executor:
// external-backend job types go to the deployment's remote backend,
// bypassing the local queue (the poller tracks them); everything else
// goes to the local scheduler. This is the single place that choice is
// made, shared by /api/run and pipeline stepping so both produce
// identical job records.
func (s *Server) submitJob(req api.JobRequest, deployManager string) (*api.JobStatus, error) {
	if s.extCapable(req.JobType) {
		job := s.d.Sched.NewJob(req, true, deployManager)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.d.Sched.SubmitExternal(ctx, job); err != nil {
			return nil, err
		}
		snap, _ := s.d.Sched.LiveCopy(job.JobID)
		return snap, nil
	}
	job := s.d.Sched.NewJob(req, true, deployManager)
	s.d.Sched.Submit(job)
	// Hand the caller a snapshot: the live record keeps being written
	// while the job runs and must not be marshalled underneath us.
	snap, _ := s.d.Sched.LiveCopy(job.JobID)
	return snap, nil
}

func (s *Server) runHandler(w http.ResponseWriter, r *http.Request) {
	var job api.JobRequest
	bodyBytes, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(bodyBytes, &job); err != nil {
		errorResponse(w, err.Error())
		slog.Error("Failed to decode job request", "error", err)
		return
	}

	deployManager := sec.GetBaseURL(r)
	snap := s.d.Sched.Snapshot()
	slog.Info("Received job request", "job_type", job.JobType, "handler", job.Handler,
		"free_cpu", snap.FreeCPU, "free_gpu", snap.FreeGPU,
		"running", snap.Running, "queued", snap.QueueLength)

	target, runLocal, forceQueue := s.d.Dispatch.Select(job, deployManager)
	switch {
	case runLocal:
		// Picked as a fallback (no idle backend) means the job must be
		// queued here rather than bounced again.
		job.ForceAddToQueue = forceQueue
		s.runLocal(w, job, deployManager)
	case target != "":
		job.ForceAddToQueue = forceQueue
		newBody, _ := json.Marshal(job)
		r.Body = io.NopCloser(bytes.NewReader(newBody))
		if body := dispatch.Forward(nil, target, w, r); body != nil {
			// Record the remote job as a redirect pointer so /api/status
			// arriving here later knows which runner owns it.
			var remote api.JobStatus
			if err := json.Unmarshal(body, &remote); err == nil && remote.JobID != "" {
				s.d.Sched.PutLive(&remote)
			}
		}
	default:
		errorResponse(w, "No suitable backends!")
	}
}

// runLocal validates and submits on this runner (the historic RunV1).
func (s *Server) runLocal(w http.ResponseWriter, job api.JobRequest, deployManager string) {
	if len(job.InputHash) != 64 {
		http.Error(w, "Invalid hash (wrong length):"+job.InputHash, http.StatusBadRequest)
		return
	}
	if s.extCapable(job.JobType) && !s.d.Sched.HasExternal() {
		http.Error(w, errExternalDisabled.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := s.validateJobRequest(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created, err := s.submitJob(job, deployManager)
	if err != nil {
		http.Error(w, "Failed to submit job: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonResponse(w, created)
}

// jobsHandler returns this runner's historic jobs, filtered by an
// optional time window (since), user, and status. Defaults to 30 days.
func (s *Server) jobsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := parseSince(q.Get("since"))
	jobs, err := s.d.Store.ListJobs(since, q.Get("user"), q.Get("status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if jobs == nil {
		jobs = []*api.JobStatus{}
	}
	jsonResponse(w, jobs)
}

// queueHandler returns the jobs currently waiting in this runner's
// queue.
func (s *Server) queueHandler(w http.ResponseWriter, r *http.Request) {
	jobs := s.d.Sched.QueueList()
	if jobs == nil {
		jobs = []*api.JobStatus{}
	}
	jsonResponse(w, jobs)
}

// activeHandler returns a representative running job, or an empty object
// when idle. The runner can run several jobs concurrently; this endpoint
// stays a single object for older CLI back-compat. Newer clients use
// /api/running for the full list (and /api/capabilities for capacity).
func (s *Server) activeHandler(w http.ResponseWriter, r *http.Request) {
	if jobs := s.d.Sched.RunningStatuses(); len(jobs) > 0 {
		jsonResponse(w, jobs[0])
		return
	}
	jsonResponse(w, &api.JobStatus{})
}

// runningHandler returns all jobs this runner is currently executing.
func (s *Server) runningHandler(w http.ResponseWriter, r *http.Request) {
	jobs := s.d.Sched.RunningStatuses()
	if jobs == nil {
		jobs = []*api.JobStatus{}
	}
	jsonResponse(w, jobs)
}

// jobStatusHandler answers status for one job: hot in-memory map first,
// then the durable store so status/logs work for jobs that aged out of
// memory or survived a runner restart. A job owned by another runner is
// answered by forwarding the request to that runner.
func (s *Server) jobStatusHandler(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobid")
	deployManager := sec.GetBaseURL(r)

	var j *api.JobStatus
	if job, ok := s.d.Sched.LiveCopy(jobID); ok {
		j = job
	} else if stored, ok := s.d.Store.GetJob(jobID); ok {
		j = stored
	} else {
		j = &api.JobStatus{
			DeployManager: deployManager,
			JobID:         jobID,
			ErrorMsg:      "Did not find job with ID: " + jobID,
		}
	}
	if j.DeployManager != deployManager { // job runs on another backend
		slog.Info("Forwarding status request to backend", "job_id", jobID, "backend", j.DeployManager)
		dispatch.Forward(nil, j.DeployManager, w, r)
	} else {
		jsonResponse(w, j)
	}
}

// cancelJobHandler cancels a queued or running job by ID. Authorization
// requires Basic Auth matching the DeployKey of the job's handler image.
// If the job is owned by another runner, the request (with its auth
// header) is forwarded there, which performs the authoritative auth
// check against its own state. 404 when no job matches, 401 when the
// DeployKey is missing or wrong.
func (s *Server) cancelJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobid")
	deployManager := sec.GetBaseURL(r)

	job, ok := s.d.Sched.Live(jobID)
	if !ok {
		http.Error(w, "no job with ID "+jobID, http.StatusNotFound)
		return
	}

	if job.DeployManager != deployManager {
		slog.Info("Forwarding cancel request to backend", "job_id", jobID, "backend", job.DeployManager)
		dispatch.Forward(nil, job.DeployManager, w, r)
		return
	}

	user, pass, hasAuth := r.BasicAuth()
	if !s.d.Auth.ValidateDeployKey(s.d.Images, job.Handler, user, pass, hasAuth) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := s.d.Sched.Cancel(jobID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
