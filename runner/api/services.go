package api

// TaskInfo is one deployed service as listed by GET /api/services (keyed by
// service name) and reported by GET /api/services/{service}.
//
// Status values: "running", "stopped", "crashed" (a service that died after
// start; its stored logs carry the reason).
const (
	TaskStatusRunning = "running"
	TaskStatusStopped = "stopped"
	TaskStatusCrashed = "crashed"
)

// TaskType values: what kind of long-running container the entry manages.
const (
	TaskTypeService = "webservice"
	TaskTypeJob     = "job"
)

type TaskInfo struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	ExternalPort int    `json:"external_port,omitempty"`
	Logs         string `json:"logs"`
	TaskType     string `json:"task_type"`
}

// ImageInfo is the public view of a deployed image (GET /api/images). It
// deliberately omits the DeployKey so listing images never leaks the
// credential that gates deletion.
type ImageInfo struct {
	Name          string
	Owner         string
	Description   string
	Documentation string
	IsService     bool
}
