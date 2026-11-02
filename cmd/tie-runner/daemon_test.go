package main

// Tests for the daemon's own machinery — the config document and its
// reload — which is the only logic this binary owns. The engine packages
// carry their own suites; the wire contract is pinned by runner/api's
// fixtures and the assembled-server tests.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/server"

	"github.com/uidbz/conf"
)

// withScratchConfig isolates the package-level config global and the
// working directory for one test. The config type carries a mutex, so
// the save/restore resets to a fresh default instead of copying a value
// that contains a lock.
func withScratchConfig(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { config = defaultServerConfig() })
	config = defaultServerConfig()
	t.Chdir(t.TempDir())
}

func TestConfigRoundTrip(t *testing.T) {
	withScratchConfig(t)

	src := ServerConfig{
		Address:            ":1163",
		TLSCert:            "tls.crt",
		TLSKey:             "tls.key",
		Filehost:           "https://fh:1166",
		Store:              "results",
		DockerRegistry:     "registry:5000",
		Capabilities:       []string{"CPUv1", "GPUv1"},
		Backends:           []string{"peer:1163"},
		BuildArgs:          map[string]string{"HTTP_PROXY": "http://proxy:3128"},
		DeployedImages:     []images.Image{{Name: "tool:v2", DeployKey: "k", IsService: true, ServiceExternalPort: 9999}},
		JobDBPath:          "jobs",
		DefaultJobMemoryMB: 4096,
		TotalCPU:           16,
		JobTypeSpecs:       map[string]executor.Spec{"CPUv1Big": {CPU: 16, RAMMB: 65536, GPU: 0}},
		AuthUser:           "tie",
		AdminToken:         "admin",
		FleetJoinToken:     "join",
		FleetSeed:          "https://seed:1163",
		ExtDir:             "/scratch",
		FileServer:         server.FileServerConfig{Enabled: true, Dir: "pub", HTTPAddress: ":8080"},
	}
	if err := src.Save(); err != nil {
		t.Fatal(err)
	}

	fresh := defaultServerConfig()
	if _, err := conf.LoadFromCurrentDir(configFileName, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Address != src.Address || fresh.Filehost != src.Filehost || fresh.Store != src.Store {
		t.Errorf("core fields lost: %+v", &fresh)
	}
	if len(fresh.Capabilities) != 2 || fresh.BuildArgs["HTTP_PROXY"] != "http://proxy:3128" {
		t.Errorf("maps/slices lost: %+v", &fresh)
	}
	if len(fresh.DeployedImages) != 1 {
		t.Fatalf("deployed image table lost: %+v", fresh.DeployedImages)
	}
	if img := fresh.DeployedImages[0]; img.Name != "tool:v2" || img.ServiceExternalPort != 9999 {
		t.Errorf("deployed image entry wrong: %+v", img)
	}
	if spec := fresh.JobTypeSpecs["CPUv1Big"]; spec.CPU != 16 || spec.RAMMB != 65536 {
		t.Errorf("job type specs lost: %+v", fresh.JobTypeSpecs)
	}
	if fresh.FleetJoinToken != "join" || fresh.ExtDir != "/scratch" {
		t.Errorf("fleet/seam fields lost: %+v", &fresh)
	}
	if !fresh.FileServer.Enabled || fresh.FileServer.Dir != "pub" || fresh.FileServer.HTTPAddress != ":8080" {
		t.Errorf("FileServer section lost: %+v", fresh.FileServer)
	}
	// Absent keys fall back to the defaults layered underneath.
	if fresh.TotalGPU != 0 {
		t.Errorf("unconfigured TotalGPU = %d, want 0 (auto-detect)", fresh.TotalGPU)
	}
}

func TestValidateConfigWarnings(t *testing.T) {
	fresh := defaultServerConfig()
	fresh.Capabilities = []string{"CPUv1", "NopeV1", "WebserviceV1"}
	fresh.JobTypeSpecs = map[string]executor.Spec{"CPUv1Huge": {CPU: 1_000_000}}
	fresh.Backends = []string{"a:1163", "", "a:1163"}

	var sb strings.Builder
	for _, w := range validateConfig(&fresh) {
		sb.WriteString(w + "\n")
	}
	joined := sb.String()
	for _, want := range []string{`"NopeV1"`, "CPUv1Huge", "empty entry", "more than once"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning set missing %q:\n%s", want, joined)
		}
	}
	// Service job types need no resource spec.
	if strings.Contains(joined, "WebserviceV1") {
		t.Errorf("service type warned:\n%s", joined)
	}
}

func TestReloadConfigSwapsHotFields(t *testing.T) {
	withScratchConfig(t)

	config = defaultServerConfig()
	config.Capabilities = []string{"CPUv1"}
	if err := config.Save(); err != nil {
		t.Fatal(err)
	}
	// Edit the file the way an operator does.
	b, err := os.ReadFile(configFileName)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.ReplaceAll(string(b), `Capabilities = ['CPUv1']`, `Capabilities = ['CPUv1', 'GPUv1']`)
	edited = strings.ReplaceAll(edited, `Address = ':1163'`, `Address = ':9999'`)
	if edited == string(b) {
		t.Fatalf("edit did not apply, file was:\n%s", b)
	}
	if err := os.WriteFile(configFileName, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := reloadConfig(); err != nil {
		t.Fatal(err)
	}
	caps := capabilitiesAccess()
	if len(caps) != 2 || caps[1] != "GPUv1" {
		t.Errorf("capabilities not swapped: %v", caps)
	}
	// Address is startup-only: it must NOT have changed live.
	if config.Address != ":1163" {
		t.Errorf("Address applied at reload: %q", config.Address)
	}

	// A broken file keeps the live config.
	if err := os.WriteFile(configFileName, []byte("Capabilities = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reloadConfig(); err == nil {
		t.Fatal("broken config must error")
	}
	if caps := capabilitiesAccess(); len(caps) != 2 {
		t.Errorf("live config changed after failed reload: %v", caps)
	}
}

func TestSaveDeployedImagesRewritesConfig(t *testing.T) {
	withScratchConfig(t)

	config = defaultServerConfig()
	if err := config.Save(); err != nil {
		t.Fatal(err)
	}
	reg := images.New(saveDeployedImages)
	if !reg.Upsert(images.Image{Name: "app:v1", DeployKey: "dk1"}) {
		t.Fatal("upsert failed")
	}
	b, err := os.ReadFile(configFileName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "app:v1") || !strings.Contains(string(b), "dk1") {
		t.Errorf("config file missing the image entry:\n%s", b)
	}
	fresh := defaultServerConfig()
	if _, err := conf.LoadFromCurrentDir(configFileName, &fresh); err != nil {
		t.Fatal(err)
	}
	if len(fresh.DeployedImages) != 1 || fresh.DeployedImages[0].Name != "app:v1" {
		t.Errorf("rewritten config unreadable: %+v", fresh.DeployedImages)
	}
}

func TestBuildHostConfig(t *testing.T) {
	withScratchConfig(t)

	config = defaultServerConfig()
	config.Filehost = "https://fh:1166"
	config.Store = "results"
	config.DockerRegistry = "registry:5000"
	config.AuthUser = "tie"
	fleetReg = fleet.Load(filepath.Join(t.TempDir(), "fleet-registry.json"))
	a := fleetReg.AddPending("seed", "https://seed:1163", []string{"CPUv1"})
	fleetReg.Approve(a.ID)
	b := fleetReg.AddPending("other", "https://other:1163", []string{"GPUv1"})
	fleetReg.Approve(b.ID)

	newHost := fleetReg.AddPending("new", "https://new:1163", []string{"CPUv1"})
	approved := fleetReg.Approve(newHost.ID) // approval mints the host's admin token
	c, ok := buildHostConfig(approved).(*ServerConfig)
	if !ok {
		t.Fatalf("host config type %T", buildHostConfig(approved))
	}
	if c.Filehost != "https://fh:1166" || c.Store != "results" || c.DockerRegistry != "registry:5000" {
		t.Errorf("shared settings not inherited: %+v", c)
	}
	if c.AdminToken != approved.AdminToken || c.AdminToken == "" {
		t.Errorf("host admin token missing: %+v", c)
	}
	if c.AuthUser != "tie" {
		t.Errorf("auth user not inherited: %q", c.AuthUser)
	}
	if c.FleetJoinToken != "" {
		t.Error("new host must not be a seed")
	}
	// Backends: every OTHER active host, never the new host itself, and
	// pending entries stay out.
	for _, u := range c.Backends {
		if u == newHost.URL {
			t.Errorf("host listed as its own backend: %v", c.Backends)
		}
	}
	joined := strings.Join(c.Backends, ",")
	if !strings.Contains(joined, "https://seed:1163") || !strings.Contains(joined, "https://other:1163") {
		t.Errorf("active hosts missing from backends: %v", c.Backends)
	}
	if len(c.Backends) != 2 {
		t.Errorf("backends = %v, want exactly the two active hosts", c.Backends)
	}
}

func TestSplitCaps(t *testing.T) {
	got := splitCaps(" CPUv1 , GPUv1,, ")
	if len(got) != 2 || got[0] != "CPUv1" || got[1] != "GPUv1" {
		t.Errorf("splitCaps = %q", got)
	}
}
