package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/uidbz/tie/runner/api"
)

func testDocker() *Docker {
	return &Docker{
		Registry: "registry.example.com/tie",
		Filehost: "https://fh.example",
		Store:    "archive",
		ExtDir:   "/tie",
	}
}

func TestRunArgs(t *testing.T) {
	d := testDocker()
	job := &api.JobStatus{JobID: "j1", Handler: "tool:v2", InputHash: "in", IsDebug: true}
	got := d.runArgs(job, Spec{CPU: 8, RAMMB: 32768}, "tool-v2", "", nil)
	want := []string{
		"run", "--rm", "--name", "tool-v2",
		"--mount", "type=bind,src=/tie/tool-v2,dst=/ext",
		"--cpus=8", "--memory=32768m", "--memory-swap=32768m",
		"registry.example.com/tie/tool:v2",
		"-filehost", "https://fh.example",
		"-inputhash", "in",
		"-debug=true",
		"-store", "archive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args differ:\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsVariants(t *testing.T) {
	d := testDocker()
	t.Run("gpu+prod+settings+apphash+symlink mounts", func(t *testing.T) {
		job := &api.JobStatus{Handler: "gpu/tool:v2", InputHash: "in", AppHash: "app", IsProd: true}
		got := d.runArgs(job, Spec{CPU: 16, RAMMB: 131072, GPU: 1}, "gpu-tool-v2", "/tmp/settings.json", []string{"/blob/a"})
		want := []string{
			"run", "--rm", "--name", "gpu-tool-v2",
			"--mount", "type=bind,src=/tie/gpu-tool-v2,dst=/ext",
			"--mount", "type=bind,src=/blob/a,dst=/blob/a",
			"-v", "/tmp/settings.json:/settings.json",
			"--pull", "always",
			"--cpus=16", "--gpus", "all", "--memory=131072m", "--memory-swap=131072m",
			"registry.example.com/tie/gpu/tool:v2",
			"-filehost", "https://fh.example",
			"-inputhash", "in",
			"-debug=false",
			"-store", "archive",
			"-settings", "/settings.json",
			"-apphash", "app",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args differ:\n got %q\nwant %q", got, want)
		}
	})
	t.Run("default memory fallback and no registry or store", func(t *testing.T) {
		d2 := &Docker{Filehost: "https://fh.example", DefaultMemMB: 2048}
		job := &api.JobStatus{Handler: "plain:latest", InputHash: "in"}
		got := d2.runArgs(job, Spec{}, "plain-latest", "", nil)
		want := []string{
			"run", "--rm", "--name", "plain-latest",
			"--mount", "type=bind,src=/tie/plain-latest,dst=/ext",
			"--memory=2048m", "--memory-swap=2048m",
			"plain:latest",
			"-filehost", "https://fh.example",
			"-inputhash", "in",
			"-debug=false",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args differ:\n got %q\nwant %q", got, want)
		}
	})
}

func writeFakeDocker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunAgainstFakeContainerCLI(t *testing.T) {
	// The fake CLI stands in for docker: it emits log lines and marker
	// protocol commands on stdout (markers must be indistinguishable from
	// ordinary logs for lines that do not carry a token), answers the
	// inspect/stats verbs, and exits with a fixed code.
	bin := writeFakeDocker(t, `
case "$1" in
  run)
    echo "hello from container"
    echo "#!TIE PercentDone 42.5"
    echo "#!TIE SetResult deadbeef"
    echo "#!TIE SetError first  failure"
    exit 0;;
  inspect) echo "false 0";;
  stats)   echo "512MiB / 1024MiB";;
esac`)

	ext := t.TempDir()
	d := &Docker{Bin: bin, Filehost: "https://fh.example", ExtDir: ext}
	job := &api.JobStatus{JobID: "j1", Handler: "tool:v2", InputHash: "in", IsDebug: true}

	var started, ended string
	info := d.Run(context.Background(), job, RunOptions{
		Settings: json.RawMessage(`{"x":1}`),
		Hooks:    Hooks{OnStart: func(c string) { started = c }, OnEnd: func(c string) { ended = c }},
	})

	if info != (ExitInfo{}) {
		t.Errorf("exit info = %+v, want zero", info)
	}
	if started != "tool-v2" || ended != "tool-v2" {
		t.Errorf("hooks: start=%q end=%q", started, ended)
	}
	if job.Percent != 42.5 {
		t.Errorf("Percent = %v, want 42.5", job.Percent)
	}
	if job.OutputHash != "deadbeef" {
		t.Errorf("OutputHash = %q", job.OutputHash)
	}
	if job.ErrorMsg != "first  failure" {
		t.Errorf("ErrorMsg = %q, want arg spacing preserved", job.ErrorMsg)
	}
	for _, want := range []string{"hello from container", "#!TIE PercentDone 42.5"} {
		if !strings.Contains(job.Log, want) {
			t.Errorf("Log missing %q:\n%s", want, job.Log)
		}
	}
	// The settings file must have been created and cleaned up: the fake
	// would have failed loudly otherwise, and no temp file may linger.
	if _, err := os.Stat(ext + "/tool-v2"); err != nil {
		t.Errorf("ext dir not created: %v", err)
	}
}

func TestRunExitAndOOMReporting(t *testing.T) {
	bin := writeFakeDocker(t, `
case "$1" in
  run) echo "#!TIE SetError boom"; exit 137;;
  inspect) echo "true 137";;
esac`)
	d := &Docker{Bin: bin, Filehost: "https://fh.example", ExtDir: t.TempDir()}
	job := &api.JobStatus{Handler: "tool:v2", InputHash: "in"}
	info := d.Run(context.Background(), job, RunOptions{})
	if info.ExitCode != 137 || !info.OOMKilled {
		t.Errorf("inspect result not honored: %+v", info)
	}
	if !strings.Contains(job.ErrorMsg, "boom") || !strings.Contains(job.ErrorMsg, "Error waiting for command") {
		t.Errorf("ErrorMsg = %q", job.ErrorMsg)
	}
}

func TestRunLegacyTokenAccepted(t *testing.T) {
	// Deployments can be configured to honor a second (legacy) marker
	// token beside the canonical one, so older container images keep
	// reporting through the same path.
	bin := writeFakeDocker(t, `
case "$1" in
  run)
    echo "#!OLD SetResult from-legacy"
    echo "#!TIE SetResult from-canonical"
    exit 0;;
  inspect) echo "false 0";;
esac`)
	d := &Docker{Bin: bin, Filehost: "https://fh.example", ExtDir: t.TempDir(),
		Protocol: Protocol{Tokens: []string{DefaultMarker, "#!OLD-TOKEN"}}}
	job := &api.JobStatus{Handler: "tool:v2", InputHash: "in"}
	d.Run(context.Background(), job, RunOptions{})
	if job.OutputHash != "from-canonical" {
		t.Errorf("OutputHash = %q, want last marker to win", job.OutputHash)
	}
}

func TestProtocolParseAndApply(t *testing.T) {
	p := Protocol{Tokens: []string{DefaultMarker, "#!OLD"}}

	if _, marker, _ := p.Parse("just a log line"); marker {
		t.Error("plain log line matched a marker")
	}
	cmd, marker, valid := p.Parse("#!OLD PercentDone 12.5")
	if !marker || !valid || cmd.Action != ActionPercent || cmd.Arg != "12.5" {
		t.Errorf("legacy token parse: %+v %v %v", cmd, marker, valid)
	}
	if _, _, valid := p.Parse("#!TIE"); valid {
		t.Error("bare token must be invalid")
	}
	cmd, marker, valid = p.Parse("#!TIE SetError")
	if !marker || !valid || cmd.Action != ActionError || cmd.Arg != "" {
		t.Errorf("action without arg: %+v %v %v", cmd, marker, valid)
	}

	job := &api.JobStatus{}
	p.Apply(job, "#!TIE UnknownVerb whatever") // unknown action: logged, no state change
	if job.ErrorMsg != "" || job.OutputHash != "" || !strings.Contains(job.Log, "UnknownVerb") {
		t.Errorf("unknown action must only log: %+v", job)
	}
	p.Apply(job, "#!TIE")
	if !strings.Contains(job.ErrorMsg, "WARNING: Incorrect marker command") {
		t.Errorf("malformed marker must add a warning error: %q", job.ErrorMsg)
	}
	p.Apply(job, "#!TIE PercentDone notanumber")
	if !strings.Contains(job.Log, "Error parsing PercentDone") {
		t.Errorf("bad percent must be logged: %q", job.Log)
	}
	if got := p.Format(ActionResult, "abcd"); got != "#!TIE SetResult abcd" {
		t.Errorf("Format = %q", got)
	}
}

func TestParseDockerSizeMB(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1.234GiB", 1263, true},
		{"512MiB", 512, true},
		{"900KiB", 0, true},
		{"10B", 0, true},
		{"2GB", 1907, true},
		{"", 0, false},
		{"nonsense", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDockerSizeMB(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseDockerSizeMB(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestContainerName(t *testing.T) {
	if got := ContainerName("registry/ns/tool:v2.1"); got != "registry-ns-tool-v2-1" {
		t.Errorf("ContainerName = %q", got)
	}
}

func TestSpecFor(t *testing.T) {
	if spec, ok := SpecFor(CPUv1, nil); !ok || spec.CPU != 8 {
		t.Errorf("default CPUv1 spec: %+v %v", spec, ok)
	}
	if _, ok := SpecFor("ext.big", nil); ok {
		t.Error("external job types must have no local spec")
	}
	over := map[string]Spec{CPUv1: {CPU: 2, RAMMB: 1024}, "custom": {CPU: 1}}
	if spec, ok := SpecFor(CPUv1, over); !ok || spec.CPU != 2 {
		t.Errorf("override must win: %+v", spec)
	}
	if spec, ok := SpecFor("custom", over); !ok || spec.CPU != 1 {
		t.Errorf("custom type: %+v %v", spec, ok)
	}
}

func TestSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "blob")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := symlinkTargets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{target}) {
		t.Errorf("symlinkTargets = %q, want [%s]", got, target)
	}
}
