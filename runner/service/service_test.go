package service

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/images"
)

// writeFakeCLI stands in for docker: it logs invocations and answers
// inspect from a state file, so start/stop/crash-watch paths run
// without a daemon.
func writeFakeCLI(t *testing.T, inspectOutput string) (bin, argsLog, stateFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "docker")
	argsLog = filepath.Join(dir, "calls.log")
	stateFile = filepath.Join(dir, "state")

	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" >> '%s'
case "$1" in
  run|stop|rm) exit 0;;
  inspect) cat '%s' 2>/dev/null;;
  logs) echo "container log line";;
esac
`, argsLog, stateFile)
	if err := os.WriteFile(stateFile, []byte(inspectOutput), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog, stateFile
}

func calls(t *testing.T, argsLog string) string {
	t.Helper()
	b, err := os.ReadFile(argsLog)
	if err != nil {
		return ""
	}
	return string(b)
}

func testManager(t *testing.T, imgs ...images.Image) (*Manager, string) {
	t.Helper()
	bin, argsLog, stateFile := writeFakeCLI(t, "true 0")
	m := New(Config{
		Images:            images.New(nil, imgs...),
		Bin:               bin,
		Registry:          "registry.example/tie",
		ExtDir:            t.TempDir(),
		Ports:             NewPortPool(2000, 2002),
		ReadinessDelay:    5 * time.Millisecond,
		ReadinessInterval: 5 * time.Millisecond,
		ReadinessTimeout:  50 * time.Millisecond,
	})
	_ = stateFile
	return m, argsLog
}

func TestPortPool(t *testing.T) {
	p := NewPortPool(3000, 3002, 3001)
	if port, err := p.Next(); err != nil || port != 3000 {
		t.Fatalf("first port = %d, %v", port, err)
	}
	p.Release(3000)
	if port, err := p.Next(); err != nil || port != 3000 { // recycled before the frontier
		t.Fatalf("recycled port = %d, %v", port, err)
	}
	if port, err := p.Next(); err != nil || port != 3002 { // 3001 reserved
		t.Fatalf("reserved port skipped? got %d, %v", port, err)
	}
	if _, err := p.Next(); err == nil {
		t.Fatal("exhaustion must error")
	}
	// Releasing a reserved port must not put a fixed service's port back
	// in circulation.
	p.Release(3001)
	if port, err := p.Next(); err == nil {
		t.Fatalf("reserved port %d leaked back into the pool", port)
	}
}

func TestStartServiceArgs(t *testing.T) {
	m, argsLog := testManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})

	msg, err := m.StartService("svc:v1")
	if err != nil {
		t.Fatal(err)
	}
	if msg != "Service svc:v1 started on port 2000" {
		t.Errorf("message = %q", msg)
	}
	want := "run\n-d\n--name\nsvc-v1\n--pull\nalways\n--mount\ntype=bind,src=" +
		m.extDir("svc-v1") + ",dst=/ext\n-p\n2000:8080\nregistry.example/tie/svc:v1\n"
	if got := calls(t, argsLog); got != want {
		t.Errorf("docker invocation:\n got %q\nwant %q", got, want)
	}
	if port, ok := m.RunningPort("svc:v1"); !ok || port != 2000 {
		t.Errorf("running port = %d %v", port, ok)
	}
	if _, err := m.StartService("svc:v1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second start must refuse: %v", err)
	}

	// Status while running attaches live logs.
	info, ok := m.Status("svc:v1")
	if !ok || info.Status != api.TaskStatusRunning || info.Logs != "container log line\n" {
		t.Errorf("status = %+v %v", info, ok)
	}
}

func TestStartServiceFixedPort(t *testing.T) {
	m, argsLog := testManager(t, images.Image{
		Name: "tcp:latest", IsService: true, ServiceInternalPort: 9000, ServiceExternalPort: 4321,
	})
	if _, err := m.StartService("tcp:latest"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls(t, argsLog), "-p\n4321:9000\n") {
		t.Errorf("fixed port not used verbatim:\n%s", calls(t, argsLog))
	}
	// Stopping keeps the reserved port out of circulation.
	if _, err := m.StopTask("tcp:latest"); err != nil {
		t.Fatal(err)
	}
	if port, err := m.cfg.Ports.Next(); err != nil || port != 2000 {
		t.Errorf("dynamic allocation = %d %v, must not reuse the fixed port", port, err)
	}
	m.cfg.Ports.Reserve(4321) // what Restore does at startup; Next must skip it
}

func TestStartUnknownImage(t *testing.T) {
	m, _ := testManager(t)
	if _, err := m.StartService("ghost:v1"); err == nil || !strings.Contains(err.Error(), "not deployed") {
		t.Errorf("unknown image: %v", err)
	}
}

func TestStopReleasesDynamicPort(t *testing.T) {
	m, _ := testManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StopTask("svc:v1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.RunningPort("svc:v1"); ok {
		t.Fatal("still marked running")
	}
	if port, err := m.cfg.Ports.Next(); err != nil || port != 2000 {
		t.Errorf("released port not recyclable: %d %v", port, err)
	}
	if _, err := m.StopTask("svc:v1"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("stop of a stopped task: %v", err)
	}
}

func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

// A service whose container dies right after start must be reported
// crashed, with its last log lines stored, and its port recycled.
func TestCrashWatch(t *testing.T) {
	bin, _, stateFile := writeFakeCLI(t, "false 7")
	m := New(Config{
		Images:            images.New(nil, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080}),
		Bin:               bin,
		ExtDir:            t.TempDir(),
		Ports:             NewPortPool(2000, 2002),
		ReadinessDelay:    5 * time.Millisecond,
		ReadinessInterval: 5 * time.Millisecond,
		ReadinessTimeout:  50 * time.Millisecond,
	})
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "crash detected", func() bool {
		info, _ := m.Status("svc:v1")
		return info.Status == api.TaskStatusCrashed
	})
	info, _ := m.Status("svc:v1")
	if !strings.Contains(info.Logs, "exit code 7") || !strings.Contains(info.Logs, "container log line") {
		t.Errorf("crash logs: %q", info.Logs)
	}
	if port, err := m.cfg.Ports.Next(); err != nil || port != 2000 {
		t.Errorf("crashed service port not recycled: %d %v", port, err)
	}
	_ = stateFile
}

// A container that stays up (even if slow) must not be declared crashed.
func TestSlowStartNotCrashed(t *testing.T) {
	m, _ := testManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // well past the probe window
	info, _ := m.Status("svc:v1")
	if info.Status != api.TaskStatusRunning {
		t.Errorf("slow starter marked %q", info.Status)
	}
}

func TestRestoreAutostartAndReserves(t *testing.T) {
	m, _ := testManager(t,
		images.Image{Name: "auto:latest", IsService: true, ServiceInternalPort: 80, ServiceExternalPort: 4430, ServiceAutostart: true},
		images.Image{Name: "manual:latest", IsService: true, ServiceInternalPort: 8080},
		images.Image{Name: "batch:latest"},
	)
	m.Restore()

	if port, ok := m.RunningPort("auto:latest"); !ok || port != 4430 {
		t.Errorf("autostart service running = %d %v", port, ok)
	}
	if _, ok := m.RunningPort("manual:latest"); ok {
		t.Error("non-autostart service must stay stopped")
	}
	tasks := m.List()
	for _, name := range []string{"auto:latest", "manual:latest"} {
		info, ok := tasks[name]
		if !ok || info.TaskType != api.TaskTypeService {
			t.Errorf("task %q registry entry: %+v %v", name, info, ok)
		}
	}
	if _, ok := tasks["batch:latest"]; ok {
		t.Error("non-service images are not tasks")
	}
	if port, err := m.cfg.Ports.Next(); err != nil || port != 2000 { // fixed port reserved
		t.Errorf("fixed port reservation: %d %v", port, err)
	}
	m.StopAll()
	if _, ok := m.RunningPort("auto:latest"); ok {
		t.Error("StopAll left a service running")
	}
}

// Running jobs appear in the same registry (task_type "job", no port):
// that is what the scheduler's OnStart/OnEnd wiring does.
func TestJobTasksAppearInRegistry(t *testing.T) {
	m, _ := testManager(t)
	jobHandler := "tool:v2"
	m.RegisterTask(jobHandler, api.TaskTypeJob)
	m.Activate(jobHandler, api.TaskTypeJob, 0)
	info, ok := m.Status(jobHandler)
	if !ok || info.Status != api.TaskStatusRunning || info.TaskType != api.TaskTypeJob || info.ExternalPort != 0 {
		t.Errorf("job task: %+v %v", info, ok)
	}
	if got := m.List()[jobHandler]; got.TaskType != api.TaskTypeJob {
		t.Errorf("list entry: %+v", got)
	}
	m.Deactivate(jobHandler)
	m.UnregisterTask(jobHandler)
	if _, ok := m.Status(jobHandler); ok {
		t.Error("unregistered task still listed")
	}
	// Status of an unknown task reports not-found, and List copies:
	// mutating a snapshot must not disturb the registry.
	snap := m.List()
	snap["injected"] = api.TaskInfo{}
	if len(m.List()) != 0 {
		t.Error("List leaked the live map")
	}
}

func TestSymlinkMountsForServices(t *testing.T) {
	m, argsLog := testManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})
	ext := m.extDir("svc-v1")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/blob/big", filepath.Join(ext, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls(t, argsLog), "--mount\ntype=bind,src=/blob/big,dst=/blob/big\n") {
		t.Errorf("symlink target not mounted:\n%s", calls(t, argsLog))
	}
}

func TestImagePublicView(t *testing.T) {
	img := images.Image{Name: "svc:v1", Owner: "o", Description: "d", Documentation: "doc", DeployKey: "secret", IsService: true}
	pub := images.PublicView(img)
	if pub.Name != "svc:v1" || !pub.IsService {
		t.Errorf("public view: %+v", pub)
	}
	if !reflect.DeepEqual(pub, api.ImageInfo{Name: "svc:v1", Owner: "o", Description: "d", Documentation: "doc", IsService: true}) {
		t.Errorf("public view fields: %+v", pub)
	}
}
