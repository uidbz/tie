package store

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/runner/api"
)

// Store is the runner's durable record of jobs and pipeline runs.
//
// The in-memory job manager remains the hot cache for live status polling;
// the store is written on the queued/running/terminal transitions and read
// back after a restart (ReconcileInterrupted, ListRunningPipelines, the
// resumable-external-jobs query) or by the list and stats endpoints.
type Store interface {
	Close() error

	// Jobs. UpsertJob writes the current state, inserting or replacing by
	// job id. GetJob returns the job with its container log; ListJobs
	// deliberately omits logs (they can be megabytes per job and no list
	// consumer reads them).
	UpsertJob(job *api.JobStatus)
	GetJob(jobID string) (*api.JobStatus, bool)
	ListJobs(sinceUnix int64, user, status string) ([]*api.JobStatus, error)
	Stats(sinceUnix int64) (*api.JobStats, error)

	// ReconcileInterrupted marks jobs left in a non-terminal state by a
	// previous process as failed, except jobs carrying an external
	// execution handle — a remote batch backend keeps running those
	// across a restart, and the poller resumes tracking them. It returns
	// how many jobs were reconciled.
	ReconcileInterrupted() (int, error)

	// ListNonTerminalExternalJobs returns jobs still non-terminal that
	// carry an external execution handle, so the poller can resume
	// tracking them after a restart.
	ListNonTerminalExternalJobs() ([]*api.JobStatus, error)

	// Pipelines. InsertPipeline writes a pipeline and all of its steps so
	// a crash can never leave a pipeline naming steps that do not exist.
	// UpsertPipelineStep records one step's progress; it is called on every
	// step state change so an interrupted runner resumes from the last
	// recorded state.
	InsertPipeline(p *api.Pipeline) error
	UpsertPipelineStep(pipelineID string, step *api.PipelineStep)
	UpdatePipelineStatus(pipelineID, status, errorMsg string, finishedUnix int64)
	GetPipeline(pipelineID string) (*api.Pipeline, bool)
	// ListPipelines returns pipelines newest first, each with its steps.
	ListPipelines(sinceUnix int64, user, status string) ([]*api.Pipeline, error)
	// ListRunningPipelines returns the pipelines a restart interrupted, so
	// recovery can advance each one instead of leaving it stalled forever.
	ListRunningPipelines() ([]*api.Pipeline, error)

	// ReapRetention deletes job and pipeline history older than keepDays
	// (0 disables). It returns how many of each were removed.
	ReapRetention(keepDays int) (jobs, pipelines int, err error)
}

// Relation names are storage-internal vocabulary. Wire spelling lives in
// runner/api and never changes; these never cross the wire.
const (
	// jobs collection
	relUser          = "user"
	relHandler       = "handler"
	relJobType       = "job-type"
	relInputHash     = "input-hash"
	relOutputHash    = "output-hash"
	relAppHash       = "app-hash"
	relStatus        = "status"
	relErrorMsg      = "error-msg"
	relLog           = "log"
	relPercent       = "percent"
	relIsProd        = "is-prod"
	relIsDebug       = "is-debug"
	relDeployManager = "deploy-manager"
	relFilehost      = "filehost"
	relCreated       = "created"
	relStarted       = "started"
	relFinished      = "finished"
	relReqCPU        = "req-cpu"
	relReqRAMMB      = "req-ram-mb"
	relMemPeakMB     = "mem-peak-mb"
	relExitReason    = "exit-reason"
	relExtRef        = "ext-ref"
	relExtQueue      = "ext-queue"
	relExtRegion     = "ext-region"
	relPipelineID    = "pipeline-id"
	relStepName      = "step-name"

	// pipelines collection
	relName  = "name"
	relDebug = "debug"
	relStep  = "step" // multi-valued: the step names this pipeline owns

	// pipeline-steps collection
	relOrdinal  = "ordinal"
	relSettings = "settings"
	relInputs   = "inputs"
	relJobID    = "job-id"
)

// stepSep joins a pipeline id and a step name into a step-subject key. Step
// names come from client submissions; the unit separator cannot appear in a
// sensible name, and the key also carries the two fields explicitly so the
// encoding of the key itself can change without losing data.
const stepSep = "\x1f"

func stepKey(pipelineID, stepName string) string {
	return pipelineID + stepSep + stepName
}

// padInt zero-pads to a fixed width so lexicographic comparison of the
// strings equals numeric comparison of the values. Width 19 covers int64
// through the heat death of practical unix timestamps. Negative values (a
// seed step's ordinal) are never range-compared, so they go through
// un-padded.
const padWidth = 19

func padInt(v int64) string {
	s := strconv.FormatInt(v, 10)
	if len(s) >= padWidth || v < 0 {
		return s
	}
	return strings.Repeat("0", padWidth-len(s)) + s
}

// numFields/boolFields drive encodeJob: presence means the value is
// meaningful; absent encodes as absent (empty strings and false booleans
// are simply not stored).
func encodeJob(j *api.JobStatus) map[string]string {
	a := map[string]string{}
	put := func(rel, v string) {
		if v != "" {
			a[rel] = v
		}
	}
	putStr := func(rel, v string) { put(rel, v) }
	putStr(relUser, j.User)
	putStr(relHandler, j.Handler)
	putStr(relJobType, j.JobType)
	putStr(relInputHash, j.InputHash)
	putStr(relOutputHash, j.OutputHash)
	putStr(relAppHash, j.AppHash)
	putStr(relStatus, j.Status)
	putStr(relErrorMsg, j.ErrorMsg)
	putStr(relLog, j.Log)
	putStr(relDeployManager, j.DeployManager)
	putStr(relFilehost, j.Filehost)
	putStr(relExitReason, j.ExitReason)
	putStr(relExtRef, j.BatchJobID)
	putStr(relExtQueue, j.Queue)
	putStr(relExtRegion, j.Region)
	putStr(relPipelineID, j.PipelineID)
	putStr(relStepName, j.StepName)

	if j.Percent != 0 {
		put(relPercent, strconv.FormatFloat(float64(j.Percent), 'f', -1, 32))
	}
	if j.IsProd {
		put(relIsProd, "1")
	}
	if j.IsDebug {
		put(relIsDebug, "1")
	}
	for _, p := range []struct {
		rel string
		v   int64
	}{{relCreated, j.CreatedUnix}, {relStarted, j.StartedUnix}, {relFinished, j.FinishedUnix},
		{relReqCPU, int64(j.ReqCPU)}, {relReqRAMMB, int64(j.ReqRAMMB)}, {relMemPeakMB, j.MemPeakMB}} {
		if p.v != 0 || p.rel == relCreated { // created is always present: it is the enumeration key
			a[p.rel] = padInt(p.v)
		}
	}
	return a
}

// decodeJob rebuilds an api.JobStatus from its subject key and field map
// (missing fields read as zero), then derives the wire-computed status
// fields exactly as the history endpoints report them.
func decodeJob(jobID string, a map[string]string) *api.JobStatus {
	j := &api.JobStatus{
		JobID:         jobID,
		User:          a[relUser],
		Handler:       a[relHandler],
		JobType:       a[relJobType],
		InputHash:     a[relInputHash],
		OutputHash:    a[relOutputHash],
		AppHash:       a[relAppHash],
		Status:        a[relStatus],
		ErrorMsg:      a[relErrorMsg],
		Log:           a[relLog],
		DeployManager: a[relDeployManager],
		Filehost:      a[relFilehost],
		ExitReason:    a[relExitReason],
		BatchJobID:    a[relExtRef],
		Queue:         a[relExtQueue],
		Region:        a[relExtRegion],
		PipelineID:    a[relPipelineID],
		StepName:      a[relStepName],
		IsProd:        a[relIsProd] != "",
		IsDebug:       a[relIsDebug] != "",
	}
	if v, err := strconv.ParseFloat(a[relPercent], 32); err == nil {
		j.Percent = float32(v)
	}
	j.CreatedUnix = parseInt(a[relCreated])
	j.StartedUnix = parseInt(a[relStarted])
	j.FinishedUnix = parseInt(a[relFinished])
	j.ReqCPU = int(parseInt(a[relReqCPU]))
	j.ReqRAMMB = int(parseInt(a[relReqRAMMB]))
	j.MemPeakMB = parseInt(a[relMemPeakMB])
	applyStatusFlags(j)
	return j
}

func encodeStep(step *api.PipelineStep) map[string]string {
	a := map[string]string{}
	put := func(rel, v string) {
		if v != "" {
			a[rel] = v
		}
	}
	a[relStepName] = step.Name
	a[relOrdinal] = padInt(int64(step.Ordinal)) // always present: it is the step order
	put(relHandler, step.Handler)
	put(relJobType, step.JobType)
	put(relAppHash, step.AppHash)
	put(relStatus, step.Status)
	put(relJobID, step.JobID)
	put(relOutputHash, step.OutputHash)
	put(relErrorMsg, step.ErrorMsg)
	if len(step.Settings) > 0 {
		a[relSettings] = string(step.Settings)
	}
	if len(step.Inputs) > 0 {
		if b, err := json.Marshal(step.Inputs); err == nil {
			a[relInputs] = string(b)
		}
	}
	for _, p := range []struct {
		rel string
		v   int64
	}{{relStarted, step.StartedUnix}, {relFinished, step.FinishedUnix}} {
		if p.v != 0 {
			a[p.rel] = padInt(p.v)
		}
	}
	return a
}

func decodeStep(a map[string]string) *api.PipelineStep {
	step := &api.PipelineStep{
		Name:       a[relStepName],
		Handler:    a[relHandler],
		JobType:    a[relJobType],
		AppHash:    a[relAppHash],
		Status:     a[relStatus],
		JobID:      a[relJobID],
		OutputHash: a[relOutputHash],
		ErrorMsg:   a[relErrorMsg],
		Ordinal:    int(parseInt(a[relOrdinal])),
	}
	if v := a[relSettings]; v != "" {
		step.Settings = json.RawMessage(v)
	}
	if v := a[relInputs]; v != "" {
		var specs []compose.Spec
		if err := json.Unmarshal([]byte(v), &specs); err == nil {
			step.Inputs = specs
		}
	}
	step.StartedUnix = parseInt(a[relStarted])
	step.FinishedUnix = parseInt(a[relFinished])
	return step
}

// encodePipeline splits a Pipeline into its own field map and the field maps
// of its steps. The pipeline's "step" relation lists step names so reading a
// pipeline never has to scan the step collection.
func encodePipeline(p *api.Pipeline) (map[string]string, map[string]map[string]string) {
	a := map[string]string{}
	put := func(rel, v string) {
		if v != "" {
			a[rel] = v
		}
	}
	put(relName, p.Name)
	put(relUser, p.User)
	put(relStatus, p.Status)
	put(relErrorMsg, p.ErrorMsg)
	put(relDeployManager, p.DeployManager)
	if p.Debug {
		a[relDebug] = "1"
	}
	a[relCreated] = padInt(p.CreatedUnix)
	if p.FinishedUnix != 0 {
		a[relFinished] = padInt(p.FinishedUnix)
	}
	steps := map[string]map[string]string{}
	names := make([]string, 0, len(p.Steps))
	for _, step := range p.Steps {
		steps[stepKey(p.PipelineID, step.Name)] = encodeStep(step)
		names = append(names, step.Name)
	}
	if len(names) > 0 {
		if b, err := json.Marshal(names); err == nil {
			a[relStep] = string(b)
		}
	}
	return a, steps
}

// stepNames reads back the step-name list an encoded pipeline carries.
func stepNames(a map[string]string) []string {
	var names []string
	if v := a[relStep]; v != "" {
		_ = json.Unmarshal([]byte(v), &names)
	}
	return names
}

func decodePipeline(a map[string]string, stepAttrs []map[string]string) *api.Pipeline {
	p := &api.Pipeline{
		Name:          a[relName],
		User:          a[relUser],
		Status:        a[relStatus],
		ErrorMsg:      a[relErrorMsg],
		DeployManager: a[relDeployManager],
		Debug:         a[relDebug] != "",
		CreatedUnix:   parseInt(a[relCreated]),
		FinishedUnix:  parseInt(a[relFinished]),
	}
	for _, sa := range stepAttrs {
		p.Steps = append(p.Steps, decodeStep(sa))
	}
	sort.SliceStable(p.Steps, func(i, j int) bool { return p.Steps[i].Ordinal < p.Steps[j].Ordinal })
	return p
}

// applyStatusFlags derives the wire-computed IsRunning/Done booleans and
// RunningTime from the persisted status and timestamps.
func applyStatusFlags(j *api.JobStatus) {
	switch j.Status {
	case api.StatusRunning:
		j.IsRunning = true
	case api.StatusDone, api.StatusFailed:
		j.Done = true
	}
	if j.StartedUnix > 0 {
		end := j.FinishedUnix
		if end == 0 {
			end = time.Now().Unix()
		}
		j.RunningTime = (time.Duration(end-j.StartedUnix) * time.Second).String()
	}
}

func parseInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// month formats a unix time the way the aggregate endpoints report months
// (UTC, "YYYY-MM").
func month(createdUnix int64) string {
	return time.Unix(createdUnix, 0).UTC().Format("2006-01")
}

// sortJobsNewestFirst orders by creation time, newest first, job id as the
// deterministic tie-break.
func sortJobsNewestFirst(jobs []*api.JobStatus) {
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].CreatedUnix != jobs[j].CreatedUnix {
			return jobs[i].CreatedUnix > jobs[j].CreatedUnix
		}
		return jobs[i].JobID > jobs[j].JobID
	})
}

func sortPipelinesNewestFirst(pipes []*api.Pipeline) {
	sort.SliceStable(pipes, func(i, j int) bool {
		if pipes[i].CreatedUnix != pipes[j].CreatedUnix {
			return pipes[i].CreatedUnix > pipes[j].CreatedUnix
		}
		return pipes[i].PipelineID > pipes[j].PipelineID
	})
}

func logStoreError(msg string, args ...any) {
	slog.Error("store: "+msg, args...)
}

// aggregateStats folds decoded job field maps into the aggregate view the
// stats endpoint serves. It mirrors the SQL GROUP BY aggregates it replaces:
// runtime statistics cover only jobs with both a start and a finish time,
// memory statistics only jobs with a recorded peak, and Running/Queued are
// left at zero for the live job manager to overlay.
func aggregateStats(rows []map[string]string) *api.JobStats {
	stats := &api.JobStats{
		ActiveUsers:         map[string][]string{},
		MonthlyUsage:        []api.MonthCount{},
		JobsByUser:          []api.NameCount{},
		ActiveUsersPerMonth: []api.MonthCount{},
		PopularImages:       []api.NameCount{},
		JobsByType:          []api.NameCount{},
	}
	byStatus := map[string]int{}
	byMonth := map[string]int{}
	byUser := map[string]int{}
	byHandler := map[string]int{}
	byType := map[string]int{}
	monthUsers := map[string]map[string]bool{}

	var runtimeSum, runtimeMax, memSum, memMax int64
	var runtimeN, memN int

	for _, a := range rows {
		status := a[relStatus]
		byStatus[status]++
		stats.Total++
		switch status {
		case api.StatusDone:
			stats.Succeeded++
		case api.StatusFailed:
			stats.Failed++
		case api.StatusCancelled:
			stats.Cancelled++
		}

		created := parseInt(a[relCreated])
		m := month(created)
		byMonth[m]++
		u := a[relUser]
		byUser[u]++
		if monthUsers[m] == nil {
			monthUsers[m] = map[string]bool{}
		}
		monthUsers[m][u] = true
		byHandler[a[relHandler]]++
		byType[a[relJobType]]++

		started, finished := parseInt(a[relStarted]), parseInt(a[relFinished])
		if started > 0 && finished > 0 {
			d := finished - started
			runtimeSum += d
			if d > runtimeMax {
				runtimeMax = d
			}
			runtimeN++
		}
		if mem := parseInt(a[relMemPeakMB]); mem > 0 {
			memSum += mem
			if mem > memMax {
				memMax = mem
			}
			memN++
		}
		if a[relExitReason] == "out-of-memory" {
			stats.OOMKills++
		}
		if a[relIsProd] != "" {
			stats.ProdJobs++
		}
		if a[relIsDebug] != "" {
			stats.DebugJobs++
		}
	}

	if runtimeN > 0 {
		stats.AvgRuntimeSeconds = float64(runtimeSum) / float64(runtimeN)
		stats.MaxRuntimeSeconds = runtimeMax
	}
	if memN > 0 {
		stats.AvgPeakMemMB = float64(memSum) / float64(memN)
		stats.MaxPeakMemMB = memMax
	}
	if terminal := stats.Succeeded + stats.Failed + stats.Cancelled; terminal > 0 {
		stats.SuccessRate = float64(stats.Succeeded) / float64(terminal)
	}

	for m, c := range byMonth {
		stats.MonthlyUsage = append(stats.MonthlyUsage, api.MonthCount{Month: m, Count: c})
	}
	sort.Slice(stats.MonthlyUsage, func(i, j int) bool { return stats.MonthlyUsage[i].Month < stats.MonthlyUsage[j].Month })

	for m, users := range monthUsers {
		list := make([]string, 0, len(users))
		for u := range users {
			list = append(list, u)
		}
		sort.Strings(list)
		stats.ActiveUsers[m] = list
		stats.ActiveUsersPerMonth = append(stats.ActiveUsersPerMonth, api.MonthCount{Month: m, Count: len(list)})
	}
	sort.Slice(stats.ActiveUsersPerMonth, func(i, j int) bool { return stats.ActiveUsersPerMonth[i].Month < stats.ActiveUsersPerMonth[j].Month })

	nameCounts := func(by map[string]int) []api.NameCount {
		out := make([]api.NameCount, 0, len(by))
		for name, c := range by {
			out = append(out, api.NameCount{Name: name, Count: c})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
	stats.JobsByUser = nameCounts(byUser)
	stats.PopularImages = nameCounts(byHandler)
	stats.JobsByType = nameCounts(byType)

	return stats
}
