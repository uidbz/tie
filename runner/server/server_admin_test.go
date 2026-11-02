package server

// Admin surface (reload/cert/fleet registry/images/services), pipeline
// endpoints, the deploy endpoint, and run/status forwarding to a remote
// runner.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/images"
)

func TestCapabilitiesFleetFilehost(t *testing.T) {
	h := newHarness(t, harnessCfg{backends: []string{"http://peer.invalid"}})
	h.filehost = "http://files.invalid"

	rec := h.do(http.MethodGet, "/api/capabilities", "", "", "")
	var caps api.CapabilityReply
	json.Unmarshal(rec.Body.Bytes(), &caps)
	if caps.Version != "v9.9.9" || caps.GitCommit != "abc123" || caps.BuildDate != "1970-01-01" {
		t.Errorf("build stamp: %+v", caps)
	}
	if len(caps.Capabilities) != 1 || caps.Capabilities[0] != "CPUv1" {
		t.Errorf("capabilities: %v", caps.Capabilities)
	}
	if !caps.IsAvailable || caps.TotalCPU != 4 {
		t.Errorf("pool: %+v", caps)
	}

	rec = h.do(http.MethodGet, "/api/fleet", "", "", "")
	var fr api.FleetReply
	json.Unmarshal(rec.Body.Bytes(), &fr)
	want := h.srv.URL
	if len(fr.Servers) != 2 || fr.Servers[0] != want || fr.Servers[1] != "http://peer.invalid" {
		t.Errorf("fleet: %+v (want self first)", fr.Servers)
	}

	rec = h.do(http.MethodGet, "/api/filehost", "", "", "")
	var fh api.DeployResponse
	json.Unmarshal(rec.Body.Bytes(), &fh)
	if fh.Message != "http://files.invalid" || fh.Store != "tie" {
		t.Errorf("filehost: %+v", fh)
	}
}

func TestReloadAuth(t *testing.T) {
	h := newHarness(t, harnessCfg{})

	if rec := h.do(http.MethodPost, "/api/reload", "", "tie", "x"); rec.Code != http.StatusForbidden && h.adminToken == "" {
		t.Errorf("unset token should 403, got %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/reload", "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no auth: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/reload", "", "tie", "admintok"); rec.Code != http.StatusOK {
		t.Fatalf("reload: %d %s", rec.Code, rec.Body.String())
	}
	if h.reloads != 1 {
		t.Errorf("reload hook calls = %d", h.reloads)
	}

	// An empty admin token disables the endpoint (403, never open).
	h.adminToken = ""
	if rec := h.do(http.MethodPost, "/api/reload", "", "tie", ""); rec.Code != http.StatusForbidden {
		t.Errorf("unset token: %d", rec.Code)
	}
}

func selfSignedPEM(t *testing.T, cn string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestCertPush(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessCfg{})
	h.certPath = filepath.Join(dir, "tls.crt")
	h.keyPath = filepath.Join(dir, "tls.key")

	certPEM, keyPEM := selfSignedPEM(t, "runner.test")
	body, _ := json.Marshal(map[string]string{"cert": certPEM, "key": keyPEM})

	if rec := h.do(http.MethodPost, "/api/cert", string(body), "tie", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong admin token: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/cert", `{"cert":"garbage"}`, "tie", "admintok"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d", rec.Code)
	}
	rec := h.do(http.MethodPost, "/api/cert", string(body), "tie", "admintok")
	if rec.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["status"] != "installed" || out["subject"] != "runner.test" || out["cert_not_after"] == "" {
		t.Errorf("push reply: %v", out)
	}
	if _, err := os.Stat(h.certPath); err != nil {
		t.Errorf("cert not written: %v", err)
	}

	rec = h.do(http.MethodGet, "/api/capabilities", "", "", "")
	var caps api.CapabilityReply
	json.Unmarshal(rec.Body.Bytes(), &caps)
	if caps.CertNotAfter == "" {
		t.Error("CertNotAfter must report the installed cert")
	}
}

func TestFleetRegistryEndpoints(t *testing.T) {
	h := newHarness(t, harnessCfg{joinToken: "join1"})

	// Register: join token gates; name+url required.
	if rec := h.do(http.MethodPost, "/api/fleet/register", `{"name":"n","url":"http://n:1163","join_token":"nope"}`, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad join token: %d", rec.Code)
	}
	rec := h.do(http.MethodPost, "/api/fleet/register", `{"name":"newbie","url":"http://n:1163","capabilities":["CPUv1"],"join_token":"join1"}`, "", "")
	var rep api.RegisterReply
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Status != api.HostPending || rep.HostID == "" || len(rep.Config) > 0 {
		t.Fatalf("register: %d %v", rec.Code, rep)
	}

	// Admin listing, approve, re-register (now active with config).
	rec = h.do(http.MethodGet, "/api/fleet/hosts", "", "tie", "admintok")
	var hosts []*api.FleetHost
	json.Unmarshal(rec.Body.Bytes(), &hosts)
	if len(hosts) != 1 || hosts[0].Status != api.HostPending {
		t.Fatalf("hosts: %+v", hosts)
	}
	rec = h.do(http.MethodPost, "/api/fleet/approve", `{"id":"`+rep.HostID+`"}`, "tie", "admintok")
	var approved api.FleetHost
	json.Unmarshal(rec.Body.Bytes(), &approved)
	if approved.Status != api.HostActive || len(approved.AdminToken) != 24 {
		t.Fatalf("approve: %+v", approved)
	}
	if rec := h.do(http.MethodPost, "/api/fleet/approve", `{"id":"`+approved.ID+`"}`, "tie", "admintok"); rec.Code != http.StatusNotFound {
		t.Errorf("approving twice must 404, got %d", rec.Code)
	}

	// Re-registering the approved URL comes back active; without a
	// BuildHostConfig hook the config stays absent.
	rec = h.do(http.MethodPost, "/api/fleet/register", `{"name":"newbie","url":"http://n:1163","join_token":"join1"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Status != api.HostActive || rep.HostID != approved.ID || len(rep.Config) > 0 {
		t.Errorf("re-register after approve: %+v", rep)
	}

	rec = h.do(http.MethodPost, "/api/fleet/remove", `{"id":"`+approved.ID+`"}`, "tie", "admintok")
	if rec.Code != http.StatusOK {
		t.Errorf("remove: %d", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/fleet/hosts", "", "tie", "admintok")
	hosts = nil
	json.Unmarshal(rec.Body.Bytes(), &hosts)
	if len(hosts) != 0 {
		t.Errorf("removed host still listed: %+v", hosts)
	}

	// A runner that is not a seed answers 404 on register.
	h2 := newHarness(t, harnessCfg{})
	if rec := h2.do(http.MethodPost, "/api/fleet/register", `{"name":"n","url":"http://n:1163","join_token":"join1"}`, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("register on non-seed: %d", rec.Code)
	}
}

// The seed's BuildHostConfig hook answers the approved re-register with
// the generated config document as raw JSON (the schema belongs to the
// binary that wires it).
func TestRegisterReplyConfigHook(t *testing.T) {
	h := newHarness(t, harnessCfg{
		joinToken:  "join1",
		hostConfig: func(host *api.FleetHost) any { return map[string]any{"AdminToken": host.AdminToken} },
	})
	rec := h.do(http.MethodPost, "/api/fleet/register", `{"name":"n","url":"http://n:1163","join_token":"join1"}`, "", "")
	var rep api.RegisterReply
	json.Unmarshal(rec.Body.Bytes(), &rep)
	h.do(http.MethodPost, "/api/fleet/approve", `{"id":"`+rep.HostID+`"}`, "tie", "admintok")

	rec = h.do(http.MethodPost, "/api/fleet/register", `{"name":"n","url":"http://n:1163","join_token":"join1"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Status != api.HostActive || len(rep.Config) == 0 {
		t.Fatalf("active reply must carry config: %s", rec.Body.String())
	}
	var cfg map[string]any
	if err := json.Unmarshal(rep.Config, &cfg); err != nil {
		t.Fatalf("config raw: %v", err)
	}
	tok, _ := cfg["AdminToken"].(string)
	if len(tok) != 24 {
		t.Errorf("config admin token: %v", cfg)
	}
}

func TestNonSeedProxiesRegistryToSeed(t *testing.T) {
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := r.BasicAuth()
		fmt.Fprintf(w, "seed saw %s auth=%v", r.URL.Path, ok)
	}))
	defer seed.Close()

	h := newHarness(t, harnessCfg{seedURL: seed.URL})
	rec := h.do(http.MethodGet, "/api/fleet/hosts", "", "tie", "admintok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "seed saw /api/fleet/hosts") {
		t.Errorf("proxy to seed: %d %s", rec.Code, rec.Body.String())
	}

	// No seed configured either: 404.
	h2 := newHarness(t, harnessCfg{})
	if rec := h2.do(http.MethodGet, "/api/fleet/hosts", "", "tie", "admintok"); rec.Code != http.StatusNotFound {
		t.Errorf("non-seed without seed: %d", rec.Code)
	}
}

func TestImagesEndpoints(t *testing.T) {
	h := newHarness(t, harnessCfg{images: []images.Image{
		{Name: "tool:v2", Owner: "me", DeployKey: "s3cret", IsService: true, ServiceInternalPort: 80},
	}})

	rec := h.do(http.MethodGet, "/api/images", "", "", "")
	if strings.Contains(rec.Body.String(), "DeployKey") || strings.Contains(rec.Body.String(), "s3cret") {
		t.Errorf("listing leaked the deploy key: %s", rec.Body.String())
	}
	var list []api.ImageInfo
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "tool:v2" || !list[0].IsService {
		t.Errorf("listing: %+v", list)
	}

	// Replicate: admin-gated, never adopts the peer's host port.
	body := `{"Name":"peer-tool:v1","DeployKey":"pk","ServiceExternalPort":1234,"IsService":true}`
	if rec := h.do(http.MethodPut, "/api/images/replicate", body, "tie", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("replicate wrong token: %d", rec.Code)
	}
	if rec := h.do(http.MethodPut, "/api/images/replicate", body, "tie", "admintok"); rec.Code != http.StatusOK {
		t.Errorf("replicate: %d %s", rec.Code, rec.Body.String())
	}
	if img, ok := h.images.Get("peer-tool:v1"); !ok || img.ServiceExternalPort != 0 {
		t.Errorf("replicated entry: %+v", img)
	}

	// Delete: deploykey gate first, then removal even though the
	// container CLI is missing (warn-only).
	if rec := h.do(http.MethodDelete, "/api/images/tool:v2", "", "tie", "nope"); rec.Code != http.StatusUnauthorized {
		t.Errorf("delete wrong key: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/images/ghost:v9", "", "tie", "s3cret"); rec.Code != http.StatusUnauthorized {
		t.Errorf("delete unknown image: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/images/tool:v2", "", "tie", "s3cret"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := h.images.Get("tool:v2"); ok {
		t.Error("image entry survived delete")
	}
}

func TestServiceEndpoints(t *testing.T) {
	h := newHarness(t, harnessCfg{images: []images.Image{
		{Name: "tool:v2", DeployKey: "s3cret", IsService: true, ServiceInternalPort: 80},
	}})

	rec := h.do(http.MethodGet, "/api/services", "", "", "")
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("empty task registry: %s", rec.Body.String())
	}
	if rec := h.do(http.MethodGet, "/api/services/ghost", "", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown service status: %d", rec.Code)
	}

	// Start gate: unknown image / wrong key are 401 at the gate; the
	// correct key reaches the handler (the missing container CLI fails
	// with 409, proving the gate let it through).
	if rec := h.do(http.MethodPost, "/api/services/ghost:v9", "", "tie", "s3cret"); rec.Code != http.StatusUnauthorized {
		t.Errorf("start unknown image: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/services/tool:v2", "", "tie", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("start wrong key: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/services/tool:v2", "", "tie", "s3cret"); rec.Code == http.StatusUnauthorized {
		t.Errorf("valid key must pass the gate, got 401: %s", rec.Body.String())
	}

	// Stop of a non-running service: 404 from the handler (gate passed).
	if rec := h.do(http.MethodDelete, "/api/services/tool:v2", "", "tie", "s3cret"); rec.Code != http.StatusNotFound {
		t.Errorf("stop not running: %d %s", rec.Code, rec.Body.String())
	}

	// Data plane without a proxy wired: historic 200 + ErrorMsg body.
	rec = h.do(http.MethodGet, "/service/tool:v2/some/path", "", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Service not running: tool:v2") {
		t.Errorf("data plane: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPipelineEndpoints(t *testing.T) {
	// Compose uploads each step's input tree; a mutable in-memory
	// filehost answers it.
	var mu sync.Mutex
	blobs := map[string]string{}
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			body, ok := blobs[strings.TrimPrefix(r.URL.Path, "/")]
			mu.Unlock()
			if !ok {
				http.Error(w, "no such blob", http.StatusNotFound)
				return
			}
			io.WriteString(w, body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			addr, err := metadata.HashReader(strings.NewReader(string(body)))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			blobs[addr] = string(body)
			mu.Unlock()
			io.WriteString(w, addr)
		}
	}))
	defer files.Close()

	h := newHarness(t, harnessCfg{
		filehost: files.URL,
		images:   []images.Image{{Name: "tool:v2", DeployKey: "s3cret"}},
	})

	// Unknown step type is rejected at submission, not mid-run.
	bad := `{"Steps":[{"Name":"prep","Handler":"tool:v2","JobType":"GHOST","InputHash":"` + goodHash + `"}]}`
	if rec := h.do(http.MethodPost, "/api/pipelines", bad, "", ""); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Unknown job type: GHOST") {
		t.Errorf("bad step type: %d %s", rec.Code, rec.Body.String())
	}

	if rec := h.do(http.MethodGet, "/api/pipelines/ghost", "", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown pipeline: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/pipelines/ghost", "", "tie", "s3cret"); rec.Code != http.StatusNotFound {
		t.Errorf("cancel unknown pipeline: %d", rec.Code)
	}

	// A one-step pipeline with an explicit input runs end to end.
	submit := `{"Steps":[{"Name":"prep","Handler":"tool:v2","JobType":"CPUv1","InputHash":"` + goodHash + `"}]}`
	rec := h.do(http.MethodPost, "/api/pipelines", submit, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	var p api.Pipeline
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p.PipelineID == "" {
		t.Fatalf("no pipeline id: %+v", p)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = h.do(http.MethodGet, "/api/pipelines/"+p.PipelineID, "", "", "")
		var got api.Pipeline
		json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Status == api.StatusDone {
			break
		}
		if time.Now().After(deadline) {
			for _, s := range got.Steps {
				t.Logf("step %s: %+v", s.Name, s)
			}
			t.Fatalf("pipeline never finished: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Resume is gated on the entry step's DeployKey; with the right key
	// the engine answers there is nothing left to rerun (409), proving
	// the request passed auth and reached the sequencer.
	if rec := h.do(http.MethodPost, "/api/pipelines/"+p.PipelineID+"/resume", "", "tie", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("resume wrong key: %d", rec.Code)
	}
	rec = h.do(http.MethodPost, "/api/pipelines/"+p.PipelineID+"/resume", "", "tie", "s3cret")
	if rec.Code != http.StatusConflict {
		t.Errorf("resume completed pipeline: %d %s", rec.Code, rec.Body.String())
	}
}

// A remote mini-runner: capable, idle, and owning the jobs it accepts.
func remoteRunner(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.CapabilityReply{
			Capabilities: []string{"CPUv1"}, FreeCPU: 8, TotalCPU: 8, IsAvailable: true,
		})
	})
	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req api.JobRequest
		json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(api.JobStatus{
			JobID: "remote-1", DeployManager: srvURL, JobType: req.JobType,
			Handler: req.Handler, Status: api.StatusQueued,
		})
	})
	mux.HandleFunc("GET /api/status/{jobid}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.JobStatus{
			JobID: r.PathValue("jobid"), DeployManager: srvURL, Status: api.StatusRunning,
		})
	})
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// When the local pool refuses (capacity 0) but a backend has room, /api/run
// forwards and remembers the redirect; a later /api/status here gets
// answered from the remote runner.
func TestRunAndStatusForwardedToBackend(t *testing.T) {
	remote := remoteRunner(t)
	h := newHarness(t, harnessCfg{backends: []string{remote.URL}, totalCPU: 1})
	// Occupy the single local CPU slot so the local runner cannot fit a
	// job right now: the spec (1 CPU) already fits when idle, so force
	// the local refusal through a queued-but-not-admitted state by
	// taking the pool down.
	if ok := h.sched.SetCapacity(0, 0, 0); !ok {
		t.Fatal("SetCapacity(0) refused")
	}

	rec := h.do(http.MethodPost, "/api/run", jobReqJSON("tool:v2"), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("forwarded run: %d %s", rec.Code, rec.Body.String())
	}
	var created api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.JobID != "remote-1" || created.DeployManager != remote.URL {
		t.Fatalf("forward reply: %+v", created)
	}

	rec = h.do(http.MethodGet, "/api/status/remote-1", "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("forwarded status: %d %s", rec.Code, rec.Body.String())
	}
	var status api.JobStatus
	json.Unmarshal(rec.Body.Bytes(), &status)
	if status.Status != api.StatusRunning || status.DeployManager != remote.URL {
		t.Errorf("forwarded status: %+v", status)
	}
}

func TestDeployEndpoints(t *testing.T) {
	// Fake filehost: one blob so the project download succeeds.
	proj := "FROM scratch\n"
	projAddr, err := metadata.HashReader(strings.NewReader(proj))
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string]string{projAddr: proj}
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimPrefix(r.URL.Path, "/")
		body, ok := blobs[hash]
		if !ok {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
	}))
	defer files.Close()

	// A container CLI that always succeeds (build/push/rmi are no-ops).
	bin := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Download failure surfaces as the historic JSON error body
	// (DeployResponse with ErrorMsg, HTTP 200).
	hf := newHarness(t, harnessCfg{filehost: "http://filehost.invalid"})
	rec := hf.do(http.MethodPost, "/api/deploymanager", `{"Command":"test","Name":"x:v1","ProjectHash":"missing"}`, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy download failure: %d", rec.Code)
	}
	var resp api.DeployResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ErrorMsg == "" {
		t.Errorf("expected ErrorMsg on download failure: %+v", resp)
	}

	h := newHarness(t, harnessCfg{
		dockerBin: bin,
		filehost:  files.URL,
		images:    []images.Image{{Name: "mine:v1", Owner: "me", DeployKey: "dk"}},
	})

	// Unknown command still round-trips the build transcript.
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"bogus","Name":"x:v1","ProjectHash":"`+projAddr+`"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ErrorMsg != "Unknown command" {
		t.Errorf("unknown command: %+v", resp)
	}

	// test: builds, then submits a non-prod job for the fresh image.
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"test","Name":"x:v1","ProjectHash":"`+projAddr+`","JobType":"CPUv1","TestInputHash":"`+goodHash+`"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ErrorMsg != "" || resp.JobStatus.JobID == "" {
		t.Errorf("test deploy: %+v", resp)
	}
	if _, ok := h.images.Get("x:v1"); ok {
		t.Error("a test deploy must not publish the image")
	}

	// test of a service type: rejected before any job.
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"test","Name":"x:v1","ProjectHash":"`+projAddr+`","JobType":"WebserviceV1"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp.ErrorMsg, "Services require --prod") {
		t.Errorf("service test deploy: %+v", resp)
	}

	// prod with a wrong key against an existing image: refused, image
	// entry untouched.
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"prod","Name":"mine:v1","Owner":"eve","ProjectHash":"`+projAddr+`","DeployKey":"nope","JobType":"CPUv1"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !strings.Contains(resp.ErrorMsg, "Wrong DeployKey provided!") || !strings.Contains(resp.ErrorMsg, "belongs to: me") {
		t.Errorf("wrong key prod deploy: %+v", resp)
	}

	// prod of a raw-TCP service: fresh deploy key generated, stable port
	// allocated from the pool, entry registered as a service.
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"prod","Name":"svc:v1","Owner":"me","ProjectHash":"`+projAddr+`","JobType":"TCPServiceV1","ServiceInternalPort":9000}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ErrorMsg != "" {
		t.Fatalf("prod deploy: %+v", resp)
	}
	if len(resp.DeployKey) < 8 || resp.ServiceExternalPort != 3000 {
		t.Errorf("prod deploy answer: %+v", resp)
	}
	if img, ok := h.images.Get("svc:v1"); !ok || !img.IsService || img.ServiceInternalPort != 9000 || img.ServiceExternalPort != 3000 || img.DeployKey != resp.DeployKey {
		t.Errorf("registered image entry: %+v", img)
	}
	if _, err := os.Stat(filepath.Join(h.extDir, executor.ContainerName("svc:v1"))); err != nil {
		t.Errorf("prod deploy must create the ext scratch dir: %v", err)
	}

	// Re-deploy with the issued key: keeps key, port, and operator
	// rewrite scope.
	h.images.Upsert(images.Image{Name: "svc:v1", Owner: "me", DeployKey: resp.DeployKey,
		IsService: true, ServiceInternalPort: 9000, ServiceExternalPort: 3000,
		ServiceProxyRewrite: "html"})
	rec = h.do(http.MethodPost, "/api/deploymanager",
		`{"Command":"prod","Name":"svc:v1","Owner":"me","ProjectHash":"`+projAddr+`","JobType":"TCPServiceV1","ServiceInternalPort":9000,"DeployKey":"`+resp.DeployKey+`"}`, "", "")
	json.Unmarshal(rec.Body.Bytes(), &resp)
	img, _ := h.images.Get("svc:v1")
	if resp.ErrorMsg != "" || img.ServiceProxyRewrite != "html" || img.ServiceExternalPort != 3000 {
		t.Errorf("re-deploy must preserve operator settings: %+v %+v", resp, img)
	}
}
