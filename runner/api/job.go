package api

import "encoding/json"

// Job status values: the vocabulary shared by jobs, pipelines, and stored
// history. Frozen wire values — never rename.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// JobRequest is a job submission: run image Handler over the input tree
// InputHash (already uploaded to the filehost) and store the output tree's
// hash on completion.
//
// PipelineID and StepName are set by pipeline stepping, not by clients, so a
// job carries its place in the graph from the moment it is created.
type JobRequest struct {
	JobType         string
	Handler         string
	InputHash       string
	AppHash         string
	Settings        json.RawMessage
	Debug           bool
	ForceAddToQueue bool
	User            string
	ReqCPU          int
	ReqRAMMB        int
	PipelineID      string
	StepName        string
}

// JobStatus is one job as reported by the runner: identity, live state,
// resource accounting, and terminal outcome. It doubles as the error body
// for some endpoints (a bare ErrorMsg on an otherwise zero value), a quirk
// preserved for wire compatibility.
type JobStatus struct {
	DeployManager string
	JobID         string
	JobType       string
	Handler       string
	RunningTime   string
	Percent       float32
	Log           string
	InputHash     string
	OutputHash    string
	AppHash       string
	ErrorMsg      string
	User          string
	Filehost      string
	Status        string
	ExitReason    string
	ReqCPU        int
	ReqRAMMB      int
	MemCurrentMB  int64
	MemPeakMB     int64
	// External execution handle: set when a remote batch backend runs the
	// job, empty when the runner executes it locally. (The wire names
	// predate this generalization; they are frozen.)
	BatchJobID string
	Queue      string
	Region     string
	// Pipeline membership: empty for standalone jobs.
	PipelineID   string
	StepName     string
	CreatedUnix  int64
	StartedUnix  int64
	FinishedUnix int64
	IsDebug      bool
	IsRunning    bool
	Done         bool
	IsProd       bool
}

// ServiceRequest wraps a JobRequest with the internal port a long-running
// service container listens on.
type ServiceRequest struct {
	Port int
	JobRequest
}

// JobStats is the aggregate view of job history and live state, returned by
// GET /api/stats and merged across the fleet.
type JobStats struct {
	Running             int
	Queued              int
	Total               int
	Succeeded           int
	Failed              int
	Cancelled           int
	SuccessRate         float64
	AvgRuntimeSeconds   float64
	MaxRuntimeSeconds   int64
	AvgPeakMemMB        float64
	MaxPeakMemMB        int64
	OOMKills            int
	ProdJobs            int
	DebugJobs           int
	MonthlyUsage        []MonthCount
	JobsByUser          []NameCount
	ActiveUsersPerMonth []MonthCount
	PopularImages       []NameCount
	JobsByType          []NameCount
	// ActiveUsers stores user sets per month for fleet merge.
	ActiveUsers map[string][]string `json:",omitempty"`
}

type MonthCount struct {
	Month string
	Count int
}

type NameCount struct {
	Name  string
	Count int
}
