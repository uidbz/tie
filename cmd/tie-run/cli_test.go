package main

// Tests for the pure CLI machinery: server/flag parsing, the project and
// config files (including the atomic key-rotation edit), lookup
// precedence, and the deploy request mapping. The wire contracts these
// depend on are pinned by the runclient and runner/server suites.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/runner/api"
)

func TestParseSinceDuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // duration.String()
		err  bool
	}{
		{"", "720h0m0s", false},    // default 30d
		{"30d", "720h0m0s", false}, //
		{"12h", "12h0m0s", false},  //
		{"90m", "1h30m0s", false},  //
		{"7d", "168h0m0s", false},  //
		{"d", "", true},            // not a number
		{"-3d", "-72h0m0s", false}, // parsed, if silly
		{"12", "", true},           // no unit
		{"30 days", "", true},      //
	} {
		got, err := parseSinceDuration(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseSinceDuration(%q): expected an error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSinceDuration(%q): %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("parseSinceDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeServer(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"runner.example.com", "https://runner.example.com"},
		{"http://localhost:1161", "http://localhost:1161"},
		{"https://r.example.com", "https://r.example.com"},
	} {
		if got := normalizeServer(tc.in); got != tc.want {
			t.Errorf("normalizeServer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseServersExplicit(t *testing.T) {
	got := parseServers("a.example.com, http://b:1161 ,")
	want := []string{"https://a.example.com", "http://b:1161"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseServers = %v, want %v", got, want)
	}

	// Empty flag with no seed configured is the "tell the user" case.
	old := defaultRunner
	defaultRunner = ""
	t.Cleanup(func() { defaultRunner = old })
	empty := parseServers("")
	if empty != nil {
		t.Errorf("parseServers(\"\") without a seed = %v, want nil", empty)
	}
	if err := requireServers(empty); err == nil {
		t.Error("requireServers on an empty fleet must error")
	}

	// An unreachable seed still yields the seed itself: single-runner
	// setups keep working while discovery is broken.
	defaultRunner = "https://seed.invalid:1"
	if got := discoverFleet(); len(got) != 1 || got[0] != defaultRunner {
		t.Errorf("discoverFleet fallback = %v, want just the seed", got)
	}
}

func TestPersistDeployKeyLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, projectFile)

	// Replaces the existing line in place; comments, unknown keys, and
	// CRLF endings survive.
	original := "# project file\r\nName = \"app:v1\"\r\nDeployKey    = 'old'\r\nSource = \"src\"\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := persistDeployKeyLine(path, "new-key"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "DeployKey = 'new-key'\r\n") {
		t.Errorf("replaced line wrong:\n%q", s)
	}
	if !strings.Contains(s, "# project file\r\n") || !strings.Contains(s, "Source = \"src\"\r\n") {
		t.Errorf("file not preserved byte-wise:\n%q", s)
	}
	if strings.Contains(s, "old") {
		t.Errorf("old key still present:\n%q", s)
	}

	// Appends when absent, and quotes a key that cannot sit inside
	// single quotes.
	path2 := filepath.Join(dir, "plain.toml")
	if err := os.WriteFile(path2, []byte("Name = \"app\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persistDeployKeyLine(path2, "it's"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path2)
	if !strings.Contains(string(b), "DeployKey = \"it's\"") {
		t.Errorf("append/quote wrong:\n%q", b)
	}
	if fi, err := os.Stat(path2); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode not preserved: %v %v", fi, err)
	}
}

func TestTieRunConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tie-run-config.toml")
	t.Setenv("TIE_RUN_CONFIG", path)

	cfg, p, err := loadTieRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	if p != path {
		t.Errorf("config path = %q, want %q", p, path)
	}
	if len(cfg.Images) != 0 {
		t.Errorf("missing config should load empty, got %v", cfg.Images)
	}

	if err := mirrorDeployKey("app:v1", "https://r1", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := mirrorDeployKey("app:v2", "https://r2", "k2"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = loadTieRunConfig()
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Images["app:v1"]; e.Runner != "https://r1" || e.DeployKey != "k1" {
		t.Errorf("entry lost on round trip: %+v", e)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("config holding DeployKeys is world-readable: %v", fi.Mode())
	}

	// A config that fails to parse is an error, not a silent miss.
	if err := os.WriteFile(path, []byte("Images = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTieRunConfig(); err == nil {
		t.Error("corrupt config must be an error")
	}
}

func TestLookupTargetPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("TIE_RUN_CONFIG", filepath.Join(dir, "cfg.toml"))

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Project file in the current directory wins when the name matches.
	write(projectFile, "Name = \"app:v1\"\nDeployKey = 'pk'\nRunner = 'https://proj-runner'\n")
	target, err := lookupTarget("app:v1", false)
	if err != nil {
		t.Fatal(err)
	}
	if target.Runner != "https://proj-runner" || target.DeployKey != "pk" || target.Source != projectFile {
		t.Errorf("project-file lookup wrong: %+v", target)
	}

	// 2. Name mismatch falls through to the synced config.
	write("tie-run-config.toml", "")
	t.Setenv("TIE_RUN_CONFIG", filepath.Join(dir, "tie-run-config.toml"))
	if err := mirrorDeployKey("other:v2", "https://cfg-runner", "ck"); err != nil {
		t.Fatal(err)
	}
	target, err = lookupTarget("other:v2", false)
	if err != nil {
		t.Fatal(err)
	}
	if target.Runner != "https://cfg-runner" || target.Source != "tie-run-config.toml" {
		t.Errorf("config lookup wrong: %+v", target)
	}

	// 3. Unknown image, not interactive: error naming both sources.
	if _, err := lookupTarget("nope:v3", false); err == nil {
		t.Error("unknown image must error when not interactive")
	} else if !strings.Contains(err.Error(), projectFile) || !strings.Contains(err.Error(), "tie-run-config.toml") {
		t.Errorf("error should name both sources: %v", err)
	}
}

func TestDeployRequestMapping(t *testing.T) {
	p := TieProject{
		Name: "app:v1", Owner: "ops", Description: "does things",
		DocumentationLink: "https://docs", DeployKey: "k", Debug: true,
		JobType: "WebserviceV1", Source: "src", TestInput: "ti", // client-only
		Runner: "https://r", ServiceInternalPort: 8080, ServiceAutostart: true,
		ServiceStartOnAccess: true, ServiceInactivityTimeout: 300,
	}
	req := p.deployRequest()
	if req.Name != "app:v1" || req.DeployKey != "k" || req.Debug != true {
		t.Errorf("mapping wrong: %+v", req)
	}
	if req.ServiceInternalPort != 8080 || !req.ServiceAutostart || !req.ServiceStartOnAccess || req.ServiceInactivityTimeout != 300 {
		t.Errorf("service settings not mapped: %+v", req)
	}
	// JobType flows to the wire DTO as-is.
	if req.JobType != "WebserviceV1" {
		t.Errorf("JobType = %q", req.JobType)
	}
}

func TestProjectRunnerAndKeyMissingFields(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if _, _, err := projectRunnerAndKey("app:v1"); err == nil {
		t.Error("missing project file must error")
	}

	if err := os.WriteFile(filepath.Join(dir, projectFile), []byte("Name = \"app:v1\"\nRunner = 'https://r'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := projectRunnerAndKey("app:v1"); err == nil || !strings.Contains(err.Error(), "DeployKey") {
		t.Errorf("missing DeployKey: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, projectFile), []byte("Name = \"app:v1\"\nDeployKey = 'k'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := projectRunnerAndKey("app:v1"); err == nil || !strings.Contains(err.Error(), "Runner") {
		t.Errorf("missing Runner: %v", err)
	}
}

func TestParseOverrides(t *testing.T) {
	got, err := parseOverrides([]string{"prep=abc", "run=def"})
	if err != nil {
		t.Fatal(err)
	}
	if got["prep"] != "abc" || got["run"] != "def" {
		t.Errorf("overrides = %v", got)
	}
	for _, bad := range [][]string{{"prep"}, {"=abc"}, {"="}} {
		if _, err := parseOverrides(bad); err == nil {
			t.Errorf("parseOverrides(%v) accepted malformed pair", bad)
		}
	}
}

func TestErrorPrefix(t *testing.T) {
	root := newRootCommand()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tie-run", "services", "stop", "x"}, "tie-run services stop:"},
		{[]string{"tie-run", "pipeline", "show", "p1"}, "tie-run pipeline show:"},
		{[]string{"tie-run", "--debug", "jobs"}, "tie-run:"}, // stops at the flag
		{[]string{"tie-run", "not-a-command", "x"}, "tie-run:"},
		{[]string{"tie-run"}, "tie-run:"},
	} {
		if got := errorPrefix(root, tc.args); got != tc.want {
			t.Errorf("errorPrefix(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestFmtHelpers(t *testing.T) {
	if got := fmtMemMB(0); got != "-" {
		t.Errorf("fmtMemMB(0) = %q", got)
	}
	if got := fmtMemMB(1024); got != "1024MB" {
		t.Errorf("fmtMemMB(1024) = %q", got)
	}
	if got := fmtCPU(0); got != "-" {
		t.Errorf("fmtCPU(0) = %q", got)
	}
	if got := fmtPort(0); got != "-" {
		t.Errorf("fmtPort(0) = %q", got)
	}
	if got := shortHash("abcdefghijklmno"); got != "abcdefghijkl" {
		t.Errorf("shortHash = %q", got)
	}
	if got := shortServer("https://r.example.com"); got != "r.example.com" {
		t.Errorf("shortServer = %q", got)
	}

	// joinCaps must not mutate the caller's slice (the CapabilityReply is
	// still used after printing).
	caps := []string{"z", "a", "m"}
	if got := joinCaps(caps); got != "a,m,z" {
		t.Errorf("joinCaps = %q", got)
	}
	if caps[0] != "z" {
		t.Errorf("joinCaps mutated the caller's slice: %v", caps)
	}
}

func TestDoneSteps(t *testing.T) {
	p := &api.Pipeline{Steps: []*api.PipelineStep{
		{Status: api.StatusDone}, {Status: api.StatusRunning}, {Status: api.StatusDone},
	}}
	if got := doneSteps(p); got != 2 {
		t.Errorf("doneSteps = %d, want 2", got)
	}
}
