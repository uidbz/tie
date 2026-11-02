package runclient

// Fleet-wide fan-out: one generic fanOut[T] and merge[T] serve every
// multi-runner listing. Results that need a "which runner said this"
// tag carry it in the Keyed wrapper, so the wire DTOs stay exactly the
// frozen runner/api shapes.

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Keyed pairs a value with the runner URL that reported it. It exists
// only client-side: the wire format never carries it.
type Keyed[T any] struct {
	Server string
	Value  T
}

// fanOut runs fetch once per server concurrently and splits results
// from errors (servers that failed), keyed by the server string as
// given.
func fanOut[T any](servers []string, fetch func(server string) (T, error)) (map[string]T, map[string]error) {
	results := make(map[string]T, len(servers))
	errs := make(map[string]error)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Go(func() {
			v, err := fetch(s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[s] = err
				return
			}
			results[s] = v
		})
	}
	wg.Wait()
	return results, errs
}

// GetFleet asks one runner for the full fleet of runner URLs via
// /api/fleet. It lets the client discover every runner from a single
// known URL instead of relying on a hard-coded list.
func GetFleet(seed string) ([]string, error) {
	var reply api.FleetReply
	if err := do(call{Method: http.MethodGet, Runner: seed, Path: "/api/fleet"}, &reply); err != nil {
		return nil, err
	}
	return reply.Servers, nil
}

// fetchJobs GETs a JSON []JobStatus from one runner endpoint.
func fetchJobs(server, path string, q url.Values) ([]*api.JobStatus, error) {
	var jobs []*api.JobStatus
	if err := do(call{Method: http.MethodGet, Runner: server, Path: path, Query: q, Timeout: listTimeout}, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// ListJobsAll queries /api/jobs on every server, merges results, and
// sorts newest first. The returned error map records servers that could
// not be reached.
func ListJobsAll(servers []string, since time.Duration, user, status string) ([]Keyed[*api.JobStatus], map[string]error) {
	perServer, errs := fanOut(servers, func(s string) ([]*api.JobStatus, error) {
		return fetchJobs(s, "/api/jobs", listQuery(since, user, status))
	})
	var out []Keyed[*api.JobStatus]
	for server, jobs := range perServer {
		for _, j := range jobs {
			out = append(out, Keyed[*api.JobStatus]{Server: server, Value: j})
		}
	}
	slices.SortFunc(out, func(a, b Keyed[*api.JobStatus]) int {
		return cmp.Compare(b.Value.CreatedUnix, a.Value.CreatedUnix) // newest first
	})
	return out, errs
}

// QueueAll returns the queued jobs per server.
func QueueAll(servers []string) (map[string][]*api.JobStatus, map[string]error) {
	return fanOut(servers, func(s string) ([]*api.JobStatus, error) {
		return fetchJobs(s, "/api/queue", nil)
	})
}

// fetchActive GETs the single actively-running job from one older
// runner.
func fetchActive(server string) (api.JobStatus, error) {
	var job api.JobStatus
	if err := do(call{Method: http.MethodGet, Runner: server, Path: "/api/active", Timeout: listTimeout}, &job); err != nil {
		return api.JobStatus{}, err
	}
	return job, nil
}

// ActiveAll returns every actively-running job per server (a runner can
// run several concurrently). It queries /api/running; for older runners
// that predate that endpoint it falls back to the single-job /api/active.
// The fallback only triggers on HTTP 404: an unreachable or failing
// runner must not pay a second round of timeouts before its real error
// is reported.
func ActiveAll(servers []string) (map[string][]*api.JobStatus, map[string]error) {
	results, errs := fanOut(servers, func(s string) ([]*api.JobStatus, error) {
		return fetchJobs(s, "/api/running", nil)
	})
	if len(errs) == 0 {
		return results, errs
	}
	stale := make([]string, 0, len(errs))
	for s, err := range errs {
		if errors.Is(err, ErrNotFound) {
			stale = append(stale, s)
		}
	}
	if len(stale) == 0 {
		return results, errs
	}
	single, singleErrs := fanOut(stale, fetchActive)
	for server, job := range single {
		delete(errs, server)
		if job.JobID != "" {
			results[server] = []*api.JobStatus{&job}
		} else {
			results[server] = []*api.JobStatus{}
		}
	}
	for server, err := range singleErrs {
		errs[server] = err
	}
	return results, errs
}

// fetchCapabilities GETs one runner's /api/capabilities reply.
func fetchCapabilities(server string) (*api.CapabilityReply, error) {
	var reply api.CapabilityReply
	if err := do(call{Method: http.MethodGet, Runner: server, Path: "/api/capabilities", Timeout: listTimeout}, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// GetVersion asks one runner for its build info via /api/capabilities.
// Runners that predate version reporting return empty Version fields.
func GetVersion(runner string) (*api.CapabilityReply, error) {
	return fetchCapabilities(runner)
}

// CapabilitiesAll queries /api/capabilities on every server
// concurrently, keyed by server, plus a per-server error map.
func CapabilitiesAll(servers []string) (map[string]*api.CapabilityReply, map[string]error) {
	return fanOut(servers, fetchCapabilities)
}

// fetchServices GETs the /api/services JSON object from one runner
// (keyed by service name) and returns it as a slice.
func fetchServices(server string) ([]api.TaskInfo, error) {
	var byName map[string]api.TaskInfo
	if err := do(call{Method: http.MethodGet, Runner: server, Path: "/api/services", Timeout: listTimeout}, &byName); err != nil {
		return nil, err
	}
	out := make([]api.TaskInfo, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	return out, nil
}

// ListServicesAll queries /api/services on every server concurrently,
// merges the results, and sorts them by server then name. The returned
// error map records servers that could not be reached.
func ListServicesAll(servers []string) ([]Keyed[api.TaskInfo], map[string]error) {
	per, errs := fanOut(servers, fetchServices)
	var out []Keyed[api.TaskInfo]
	for server, services := range per {
		for _, s := range services {
			out = append(out, Keyed[api.TaskInfo]{Server: server, Value: s})
		}
	}
	slices.SortFunc(out, func(a, b Keyed[api.TaskInfo]) int {
		return cmp.Or(cmp.Compare(a.Server, b.Server), cmp.Compare(a.Value.Name, b.Value.Name))
	})
	return out, errs
}

// fetchPipelines GETs a JSON []Pipeline from one runner.
func fetchPipelines(server string, q url.Values) ([]*api.Pipeline, error) {
	var ps []*api.Pipeline
	if err := do(call{Method: http.MethodGet, Runner: server, Path: "/api/pipelines", Query: q, Timeout: listTimeout}, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// ListPipelinesAll queries /api/pipelines on every server, merges
// results, and sorts newest first. The returned error map records
// servers that could not be reached.
func ListPipelinesAll(servers []string, since time.Duration, user, status string) ([]Keyed[*api.Pipeline], map[string]error) {
	per, errs := fanOut(servers, func(s string) ([]*api.Pipeline, error) {
		return fetchPipelines(s, listQuery(since, user, status))
	})
	var out []Keyed[*api.Pipeline]
	for server, ps := range per {
		for _, p := range ps {
			out = append(out, Keyed[*api.Pipeline]{Server: server, Value: p})
		}
	}
	slices.SortFunc(out, func(a, b Keyed[*api.Pipeline]) int {
		return cmp.Compare(b.Value.CreatedUnix, a.Value.CreatedUnix) // newest first
	})
	return out, errs
}

// fetchPipeline GETs one pipeline from a single runner. A runner that
// does not own the pipeline answers 404, which do() surfaces as an error
// wrapping ErrNotFound.
func fetchPipeline(server, id string) (api.Pipeline, error) {
	var p api.Pipeline
	if err := do(call{Method: http.MethodGet, Runner: server, Path: "/api/pipelines/" + seg(id)}, &p); err != nil {
		return api.Pipeline{}, err
	}
	return p, nil
}

// FindPipeline locates a pipeline by ID across the given runners. A
// pipeline is owned by the runner that accepted it, so every other
// runner 404s; the first hit wins. Errors other than 404 are reported so
// an unreachable fleet is not mistaken for "the pipeline does not
// exist".
func FindPipeline(servers []string, id string) (Keyed[api.Pipeline], error) {
	results, errs := fanOut(servers, func(s string) (api.Pipeline, error) {
		return fetchPipeline(s, id)
	})
	for server, p := range results {
		return Keyed[api.Pipeline]{Server: server, Value: p}, nil
	}
	var hard []error
	for _, err := range errs {
		if !errors.Is(err, ErrNotFound) {
			hard = append(hard, err)
		}
	}
	if len(hard) > 0 {
		return Keyed[api.Pipeline]{}, fmt.Errorf("pipeline %s not found on any reachable runner (%v)", id, hard)
	}
	return Keyed[api.Pipeline]{}, fmt.Errorf("pipeline %s not found on any runner", id)
}
