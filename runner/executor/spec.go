package executor

// Standard local job types. The names are wire-visible (clients submit
// them in JobRequest.JobType and fleets advertise them in capabilities),
// so they are frozen: never rename, only add.
const (
	GPUv1             = "GPUv1"
	GPUv1HighMem      = "GPUv1HighMem"
	CPUv1             = "CPUv1"
	CPUv1LongRunning  = "CPUv1LongRunning"
	GPUv1Async        = "GPUv1Async"
	GPUv1HighMemAsync = "GPUv1HighMemAsync"
	CPUv1Async        = "CPUv1Async"

	// Long-running task types (kept here so sizing vocabulary lives in
	// one place; the service machinery interprets them).
	WEBSERVICE = "WebserviceV1"
	TCPSERVICE = "TCPServiceV1"
)

// Spec is the per-container resource sizing for a local job type. It is
// enforced on `docker run` (--cpus/--memory/--gpus) and used by the
// scheduler to admit or queue jobs against the runner's resource pool.
type Spec struct {
	CPU   int // cores (docker --cpus)
	RAMMB int // memory in MB (docker --memory); 0 falls back to the configured default
	GPU   int // GPUs required; >0 adds --gpus all
}

// DefaultSpecs returns the built-in sizing for every local job type, so
// deployments that configure no overrides keep working. GPU types default
// to GPU = 1 (one GPU job at a time unless the pool is raised).
func DefaultSpecs() map[string]Spec {
	return map[string]Spec{
		CPUv1:             {CPU: 8, RAMMB: 32768, GPU: 0},
		CPUv1LongRunning:  {CPU: 8, RAMMB: 32768, GPU: 0},
		CPUv1Async:        {CPU: 8, RAMMB: 32768, GPU: 0},
		GPUv1:             {CPU: 8, RAMMB: 32768, GPU: 1},
		GPUv1Async:        {CPU: 8, RAMMB: 32768, GPU: 1},
		GPUv1HighMem:      {CPU: 16, RAMMB: 131072, GPU: 1},
		GPUv1HighMemAsync: {CPU: 16, RAMMB: 131072, GPU: 1},
	}
}

// SpecFor returns the sizing for a job type, with the override map merged
// over the built-in defaults. The bool is false for types with no spec
// (external-backend or service types), which are not locally sized.
func SpecFor(jobType string, overrides map[string]Spec) (Spec, bool) {
	if spec, ok := overrides[jobType]; ok {
		return spec, true
	}
	spec, ok := DefaultSpecs()[jobType]
	return spec, ok
}
