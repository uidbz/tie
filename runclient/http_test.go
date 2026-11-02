package runclient

// Pins for the shared HTTP helper and the fleet/error semantics: typed
// 401/404 sentinels, normalisation inside do(), seg() escaping, the
// per-server timeouts, and the request shapes of the keyed endpoints.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
)

func TestHTTPErrorUnwrapSentinels(t *testing.T) {
	e401 := &HTTPError{Method: "GET", URL: "http://r/api/jobs/1", Status: http.StatusUnauthorized}
	if !errors.Is(e401, ErrUnauthorized) {
		t.Errorf("401 does not unwrap to ErrUnauthorized: %v", e401)
	}
	if errors.Is(e401, ErrNotFound) {
		t.Errorf("401 must not unwrap to ErrNotFound")
	}
	e404 := &HTTPError{Method: "GET", URL: "http://r/api/pipelines/p1", Status: http.StatusNotFound}
	if !errors.Is(e404, ErrNotFound) {
		t.Errorf("404 does not unwrap to ErrNotFound: %v", e404)
	}
	e500 := &HTTPError{Method: "GET", URL: "http://r/api/jobs", Status: http.StatusInternalServerError}
	if errors.Is(e500, ErrNotFound) || errors.Is(e500, ErrUnauthorized) {
		t.Errorf("500 must not unwrap to a sentinel: %v", e500)
	}
	// The message carries method, URL, status and body for the terminal.
	e := &HTTPError{Method: "POST", URL: "http://r/api/run", Status: 422, Body: "bad step"}
	if s := e.Error(); s != "POST http://r/api/run: 422: bad step" {
		t.Errorf("Error() = %q", s)
	}
}

// TestDoNormalisesTrailingSlash pins that do() trims a trailing "/" from
// a configured runner URL: the bug class must not return on a new
// endpoint.
func TestDoNormalisesTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := do(call{Method: http.MethodGet, Runner: srv.URL + "/", Path: "/api/filehost"}, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotPath != "/api/filehost" {
		t.Errorf("path = %q, want /api/filehost (no double slash)", gotPath)
	}
}

// TestDoTypedErrorsOnWire pins that a 401/404 from the runner reaches
// callers as errors.Is-able sentinels through do() itself.
func TestDoTypedErrorsOnWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/unauthorized":
			http.Error(w, "nope", http.StatusUnauthorized)
		case "/api/missing":
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	err := do(call{Method: http.MethodGet, Runner: srv.URL, Path: "/api/unauthorized"}, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401 via do: %v, want errors.Is(err, ErrUnauthorized)", err)
	}
	err = do(call{Method: http.MethodGet, Runner: srv.URL, Path: "/api/missing"}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("404 via do: %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestSegEscapesPathSegments pins that a registry-qualified image name
// or an ID with query/fragment characters cannot escape its path
// segment.
func TestSegEscapesPathSegments(t *testing.T) {
	if got := seg("registry/ns/img:tag"); got != "registry%2Fns%2Fimg:tag" {
		t.Errorf("seg(registry/ns/img:tag) = %q, want registry%%2Fns%%2Fimg:tag", got)
	}
	if got := seg("a b?c#d"); got != "a%20b%3Fc%23d" {
		t.Errorf("seg(a b?c#d) = %q, want a%%20b%%3Fc%%23d", got)
	}
	// Plain names stay byte-identical: the wire format for today's names
	// (tool:v2, job IDs) must not change.
	if got := seg("tool:v2"); got != "tool:v2" {
		t.Errorf("seg(tool:v2) = %q, want unchanged", got)
	}
}

// TestSetServiceEscapesName pins the end-to-end effect of seg(): the
// wire request carries the escaped form, so a registry-qualified name
// stays one path segment instead of becoming extra route depth.
func TestSetServiceEscapesName(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI // raw, undecoded request target
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := SetService(srv.URL, "registry/ns/img:tag", "key", true); err != nil {
		t.Fatalf("SetService: %v", err)
	}
	if !strings.Contains(gotURI, "registry%2Fns%2Fimg:tag") {
		t.Errorf("request URI = %q, want the slash escaped as %%2F", gotURI)
	}
}

// TestSetServiceRequestShape pins the wire format of service start/stop:
// DELETE/POST {runner}/api/services/{name} with Basic auth
// AuthUser:<key>.
func TestSetServiceRequestShape(t *testing.T) {
	var gotMethod, gotPath, gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := SetService(srv.URL+"/", "myapp:latest", " key123 ", false); err != nil {
		t.Fatalf("SetService returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if gotPath != "/api/services/myapp:latest" {
		t.Errorf("path = %q, want /api/services/myapp:latest (no double slash)", gotPath)
	}
	if gotUser != AuthUser || gotPass != "key123" {
		t.Errorf("auth = %q/%q, want %s/key123 (key must be trimmed)", gotUser, gotPass, AuthUser)
	}
}

// TestAuthUserConfigurable pins the deployment hook: a branded shell
// sets runclient.AuthUser and every keyed request follows.
func TestAuthUserConfigurable(t *testing.T) {
	old := AuthUser
	AuthUser = "custom"
	defer func() { AuthUser = old }()

	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := CancelJob(srv.URL, "j1", "k"); err != nil {
		t.Fatal(err)
	}
	if gotUser != "custom" {
		t.Errorf("auth user = %q, want custom", gotUser)
	}
}

// TestSetServiceDebugMode pins that TIE_DEBUG does not break the call.
func TestSetServiceDebugMode(t *testing.T) {
	t.Setenv("TIE_DEBUG", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := SetService(srv.URL, "myapp:latest", "key123", true); err != nil {
		t.Fatalf("SetService with TIE_DEBUG failed: %v", err)
	}
}

// TestListTimeoutPinned pins the per-server timeout for fleet list
// commands: one dead runner must not stall a jobs listing for the full
// default.
func TestListTimeoutPinned(t *testing.T) {
	if listTimeout != 10*time.Second {
		t.Errorf("listTimeout = %v, want 10s", listTimeout)
	}
	if defaultTimeout != 30*time.Second {
		t.Errorf("defaultTimeout = %v, want 30s", defaultTimeout)
	}
}

// TestListFetcherHonoursShortTimeout proves a list fetcher gives up on a
// hanging runner in about listTimeout, not the 30s default.
func TestListFetcherHonoursShortTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answer; wait for the client to give up
	}))
	defer srv.Close()

	start := time.Now()
	_, err := fetchServices(srv.URL)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("fetchServices against a hanging runner returned nil")
	}
	if elapsed > 2*listTimeout {
		t.Errorf("fetchServices took %v, want ~listTimeout (%v)", elapsed, listTimeout)
	}
}

// TestDoUnmarshalErrorCarriesBody pins that a 200 with a non-JSON body
// is reported as a parse error naming the body excerpt, not a bare
// syntax error.
func TestDoUnmarshalErrorCarriesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>gateway</html>"))
	}))
	defer srv.Close()

	var out api.FleetReply
	err := do(call{Method: http.MethodGet, Runner: srv.URL, Path: "/api/fleet"}, &out)
	if err == nil {
		t.Fatal("expected a parse error for an HTML body")
	}
	var he *HTTPError
	if errors.As(err, &he) {
		t.Errorf("200 must not become an HTTPError: %v", err)
	}
	if s := err.Error(); !strings.Contains(s, "parsing reply") || !strings.Contains(s, "<html>gateway</html>") {
		t.Errorf("error %q should name the parse failure and the body", s)
	}
}

// --- GetFilehostStore error handling --------------------------------------

func TestGetFilehostStoreOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/filehost" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(api.DeployResponse{Message: "http://filehost:1234", Store: "store1"})
	}))
	defer srv.Close()

	fh, store, err := GetFilehostStore(srv.URL)
	if err != nil {
		t.Fatalf("GetFilehostStore: %v", err)
	}
	if fh != "http://filehost:1234" || store != "store1" {
		t.Errorf("got filehost=%q store=%q", fh, store)
	}
}

// TestGetFilehostStore500 pins that an HTTP 5xx is an error carrying the
// status code, not a silent success with empty strings.
func TestGetFilehostStore500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusInternalServerError)
	}))
	defer srv.Close()

	fh, store, err := GetFilehostStore(srv.URL)
	if err == nil {
		t.Fatalf("expected an error on 500, got filehost=%q store=%q", fh, store)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not mention the status code", err)
	}
	if fh != "" || store != "" {
		t.Errorf("expected empty results on error, got %q %q", fh, store)
	}
}

// TestGetFilehostStoreUnreachable pins that a transport failure
// surfaces as the real network error, not "unexpected end of JSON
// input".
func TestGetFilehostStoreUnreachable(t *testing.T) {
	// Port 1 on loopback: connection refused without leaving the host.
	_, _, err := GetFilehostStore("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected an error for an unreachable runner")
	}
	if strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Errorf("network error masked as a JSON error: %v", err)
	}
}

// TestGetFilehostStoreErrorMsg keeps the runner-level ErrorMsg path
// working.
func TestGetFilehostStoreErrorMsg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.DeployResponse{ErrorMsg: "filehost not configured"})
	}))
	defer srv.Close()

	_, _, err := GetFilehostStore(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "filehost not configured") {
		t.Errorf("expected the runner's ErrorMsg surfaced, got %v", err)
	}
}

// --- pipeline cancel/resume request shape ---------------------------------

func TestCancelPipelineRequestShape(t *testing.T) {
	var gotMethod, gotPath, gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := CancelPipeline(srv.URL+"/", "p1", " key123 "); err != nil {
		t.Fatalf("CancelPipeline returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if gotPath != "/api/pipelines/p1" {
		t.Errorf("path = %q, want /api/pipelines/p1 (no double slash)", gotPath)
	}
	if gotUser != AuthUser || gotPass != "key123" {
		t.Errorf("auth = %q/%q, want %s/key123", gotUser, gotPass, AuthUser)
	}
}

func TestResumePipelineRequestShape(t *testing.T) {
	var gotMethod, gotPath, gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, gotPass, _ = r.BasicAuth()
		json.NewEncoder(w).Encode(api.Pipeline{PipelineID: "p1", Status: "running"})
	}))
	defer srv.Close()

	if _, err := ResumePipeline(srv.URL+"/", "p1", " key123 ", "", nil); err != nil {
		t.Fatalf("ResumePipeline returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/pipelines/p1/resume" {
		t.Errorf("path = %q, want /api/pipelines/p1/resume (no double slash)", gotPath)
	}
	if gotUser != AuthUser || gotPass != "key123" {
		t.Errorf("auth = %q/%q, want %s/key123", gotUser, gotPass, AuthUser)
	}
}

// --- fleet helpers treat HTTP errors as errors -----------------------------

// TestListJobsAll503IsError pins that a 503 whose body happens to be
// valid JSON ("[]") is reported as a per-server error, not as "no jobs".
func TestListJobsAll503IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	jobs, errs := ListJobsAll([]string{srv.URL}, 0, "", "")
	if len(errs) != 1 {
		t.Fatalf("expected 1 server error for a 503, got errs=%v jobs=%d", errs, len(jobs))
	}
	err, ok := errs[srv.URL]
	if !ok || !strings.Contains(err.Error(), "503") {
		t.Errorf("error not tagged to the failing server or missing the status: %v", errs)
	}
	if len(jobs) != 0 {
		t.Errorf("expected no jobs, got %d", len(jobs))
	}
}

// TestActiveAllFallsBackOnlyOn404 pins: /api/active is consulted only
// when /api/running 404s (an old runner). A 500 or an unreachable runner
// must report its real error without a second round of requests.
func TestActiveAllFallsBackOnlyOn404(t *testing.T) {
	var activeHits int
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/running":
			http.Error(w, "not here yet", http.StatusNotFound)
		case "/api/active":
			activeHits++
			json.NewEncoder(w).Encode(api.JobStatus{JobID: "j1", Status: "running"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer old.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/active" {
			t.Errorf("fallback to /api/active for a non-404 server")
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()

	results, errs := ActiveAll([]string{old.URL, broken.URL})
	if activeHits != 1 {
		t.Errorf("/api/active hit %d times, want exactly 1 (only the 404 server)", activeHits)
	}
	if len(results[old.URL]) != 1 || results[old.URL][0].JobID != "j1" {
		t.Errorf("404 server should have fallen back to /api/active: %+v", results)
	}
	err, ok := errs[broken.URL]
	if !ok || !strings.Contains(err.Error(), "500") {
		t.Errorf("500 server should report its status error, got errs=%v", errs)
	}
}

// --- one since format for jobs and pipelines --------------------------------

func TestSinceParamFormat(t *testing.T) {
	if got := sinceParam(720 * time.Hour); got != "2592000s" {
		t.Errorf("sinceParam(720h) = %q, want 2592000s (same format as jobs)", got)
	}
}

// TestListPipelinesAllSinceFormat pins the wire format of the pipeline
// since filter against the request the runner actually receives.
func TestListPipelinesAllSinceFormat(t *testing.T) {
	var gotSince string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSince = r.URL.Query().Get("since")
		json.NewEncoder(w).Encode([]api.Pipeline{})
	}))
	defer srv.Close()

	_, errs := ListPipelinesAll([]string{srv.URL}, 720*time.Hour, "", "")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if gotSince != "2592000s" {
		t.Errorf("since = %q, want 2592000s (jobs format, not 720h0m0s)", gotSince)
	}
}

// TestCapabilitiesAllKeysServers keeps the fan-out keying contract.
func TestCapabilitiesAllKeysServers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.CapabilityReply{Capabilities: []string{"CPUv1"}, TotalCPU: 8})
	}))
	defer srv.Close()

	caps, errs := CapabilitiesAll([]string{srv.URL})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if caps[srv.URL] == nil || caps[srv.URL].TotalCPU != 8 {
		t.Errorf("capabilities by server lost: %+v", caps)
	}
}
