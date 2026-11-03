package fleetcmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerapi "github.com/uidbz/tie/runner/api"
)

func TestLoadFleetSeedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tie-fleet.toml")
	src := `seed = "https://seed.example"

[[Hosts]]
Name = "a"
API = "https://a.example"
Init = "openrc"
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFleetConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Seed != "https://seed.example" {
		t.Fatalf("seed: %q", cfg.Seed)
	}
	if cfg.Hosts[0].Init != "openrc" {
		t.Fatalf("init: %q", cfg.Hosts[0].Init)
	}
}

func TestServiceCmd(t *testing.T) {
	svc, pid := serviceName(), pidFile()
	cases := []struct{ init, action, want string }{
		{"", "restart", "sudo systemctl restart tie-runner"},
		{"systemd", "reload", "sudo systemctl reload tie-runner"},
		{"openrc", "restart", "sudo rc-service tie-runner restart"},
		{"openrc", "stop", "sudo rc-service tie-runner stop"},
		{"openrc", "reload", "sudo kill -HUP $(cat /run/tie-runner.pid)"},
	}
	for _, c := range cases {
		if got := serviceCmd(c.init, c.action, svc, pid); got != c.want {
			t.Errorf("serviceCmd(%q,%q) = %q, want %q", c.init, c.action, got, c.want)
		}
	}
}

func TestRegistryListAndApprove(t *testing.T) {
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != brand.AuthUser || p != "adm" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/fleet/hosts":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": "abc123def4", "name": "box1", "url": "https://box1", "status": "pending"},
				{"id": "fff000aaa1", "name": "box2", "url": "https://box2", "status": "active"},
			})
		case "/api/fleet/approve":
			json.NewEncoder(w).Encode(map[string]any{"id": "abc123def4", "status": "active"})
		default:
			http.Error(w, r.URL.Path, 404)
		}
	}))
	defer seed.Close()

	cl, err := newAPI("", brand.AuthUser)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := cl.fleetHosts(seed.URL, "adm")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0].ID != "abc123def4" || hosts[0].Status != "pending" {
		t.Fatalf("hosts: %+v", hosts)
	}
	if err := cl.fleetApprove(seed.URL, "adm", "abc123def4"); err != nil {
		t.Fatal(err)
	}
	// Wrong token => error.
	if _, err := cl.fleetHosts(seed.URL, "wrong"); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestFormatHostsTable(t *testing.T) {
	hosts := []runnerapi.FleetHost{
		{ID: "id1", Name: "box1", URL: "https://box1", Status: "pending", Capabilities: []string{"CPUv1", "GPUv1"}},
		{ID: "id2", Name: "box2", URL: "https://box2", Status: "active"},
	}
	out := formatHostsTable(hosts)
	for _, want := range []string{"id1", "box1", "pending", "CPUv1,GPUv1", "id2", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestMergeRegistryHosts(t *testing.T) {
	hosts := []Host{{Name: "runner1", API: "https://runner1"}}
	registry := []runnerapi.FleetHost{
		{ID: "1", Name: "runner1", URL: "https://runner1", Status: "active"}, // dup: TOML wins
		{ID: "2", Name: "box2", URL: "https://box2", Status: "active"},       // merged in
		{ID: "3", Name: "box3", URL: "https://box3", Status: "pending"},      // not merged
	}
	got := mergeRegistryHosts(hosts, registry)
	if len(got) != 2 {
		t.Fatalf("want 2 hosts, got %+v", got)
	}
	if got[0].Name != "runner1" || got[1].Name != "box2" || got[1].API != "https://box2" {
		t.Fatalf("merge wrong: %+v", got)
	}
}

func TestCertStatusRow(t *testing.T) {
	// certStatusRow formats one status line; <30d expiry flags a warning.
	soon := time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	far := time.Now().Add(200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if line := certStatusRow("box1", soon); !strings.Contains(line, "EXPIRES") {
		t.Errorf("soon cert not flagged: %s", line)
	}
	if line := certStatusRow("box2", far); strings.Contains(line, "EXPIRES") {
		t.Errorf("far cert flagged: %s", line)
	}
	if line := certStatusRow("box3", ""); !strings.Contains(line, "no TLS cert") {
		t.Errorf("empty cert: %s", line)
	}
}

func TestCertPushClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cert" {
			http.Error(w, r.URL.Path, 404)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != brand.AuthUser || p != "adm" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		if req["cert"] == "" || req["key"] == "" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "installed", "cert_not_after": "2027-01-01"})
	}))
	defer srv.Close()
	cl, _ := newAPI("", brand.AuthUser)
	if err := cl.certPush(srv.URL, "adm", "PEMCERT", "PEMKEY"); err != nil {
		t.Fatal(err)
	}
	if err := cl.certPush(srv.URL, "wrong", "PEMCERT", "PEMKEY"); err == nil {
		t.Fatal("expected auth error")
	}
}
