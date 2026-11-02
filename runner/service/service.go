// Package service manages long-running containers: webservices (reached
// through the reverse proxy on a dynamic host port) and raw-TCP services
// (reached directly on a stable reserved host port). It owns the task
// registry — the live view of services and running jobs that the status
// endpoints report — and the host-port pool.
//
// A started service is watched for boot crashes: "container created" is
// not "service up", and an app that dies immediately used to be
// reported as running until someone checked by hand. The watcher probes
// the container after start; if it exited, the last log lines are stored
// on the task and the port goes back to the pool. Containers are not
// started with --rm precisely so those logs survive.
package service

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/images"
)

// Config wires the manager.
type Config struct {
	// Images is the deployed-image registry: services start from image
	// entries (internal port, fixed port, autostart).
	Images *images.Registry
	// Bin is the container CLI ("docker"). Configurable so deployments
	// can point at a wrapper and tests can fake it.
	Bin string
	// Registry prefixes the image reference ("" means name as-is).
	Registry string
	// ExtDir is the host-side scratch base for service containers
	// (default "/tie"), bind-mounted at /ext like job containers.
	ExtDir string
	// Ports is the dynamic port pool; nil uses 2000-65535.
	Ports *PortPool
	// ReadinessDelay/Interval/Timeout bound the post-start crash watch
	// (defaults 3s / 1s / 10s). Only a container that EXITED inside the
	// window is declared crashed — slow starters are left alone.
	ReadinessDelay    time.Duration
	ReadinessInterval time.Duration
	ReadinessTimeout  time.Duration
	// LogsTail is how many log lines status attaches (default 200).
	LogsTail int
}

// Manager is the task registry plus the service runtime.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	tasks   map[string]*api.TaskInfo // every registered task (services and running jobs)
	running map[string]int           // task name -> published host port
}

// New builds a manager and applies the pool defaults.
func New(cfg Config) *Manager {
	if cfg.Bin == "" {
		cfg.Bin = "docker"
	}
	if cfg.ExtDir == "" {
		cfg.ExtDir = "/tie"
	}
	if cfg.Ports == nil {
		cfg.Ports = NewPortPool(2000, 65535)
	}
	if cfg.ReadinessDelay == 0 {
		cfg.ReadinessDelay = 3 * time.Second
	}
	if cfg.ReadinessInterval == 0 {
		cfg.ReadinessInterval = 1 * time.Second
	}
	if cfg.ReadinessTimeout == 0 {
		cfg.ReadinessTimeout = 10 * time.Second
	}
	if cfg.LogsTail == 0 {
		cfg.LogsTail = 200
	}
	return &Manager{cfg: cfg, tasks: map[string]*api.TaskInfo{}, running: map[string]int{}}
}

func (m *Manager) extDir(name string) string { return m.cfg.ExtDir + "/" + name }

// imageRef resolves the image reference for a deployed name.
func (m *Manager) imageRef(name string) string {
	if m.cfg.Registry == "" {
		return name
	}
	return strings.TrimSuffix(m.cfg.Registry, "/") + "/" + name
}

// RegisterTask adds a task to the registry (services at deploy time,
// jobs while they run). Re-registering an existing name keeps its state.
func (m *Manager) RegisterTask(name, taskType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[name]; !exists {
		m.tasks[name] = &api.TaskInfo{Name: name, Status: api.TaskStatusStopped, TaskType: taskType}
	}
}

// UnregisterTask removes a task (image deleted).
func (m *Manager) UnregisterTask(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, name)
	delete(m.running, name)
}

// Activate marks a task running on a published port (0 for jobs, which
// publish nothing).
func (m *Manager) Activate(name, taskType string, externalPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running[name] = externalPort
	if _, exists := m.tasks[name]; !exists {
		m.tasks[name] = &api.TaskInfo{Name: name}
	}
	m.tasks[name].Status = api.TaskStatusRunning
	m.tasks[name].TaskType = taskType
	m.tasks[name].ExternalPort = externalPort
}

// Deactivate marks a task stopped and forgets its port.
func (m *Manager) Deactivate(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, name)
	if info, ok := m.tasks[name]; ok {
		info.Status = api.TaskStatusStopped
		info.ExternalPort = 0
	}
}

// RunningPort returns the published host port of a running service (the
// reverse proxy routes to it).
func (m *Manager) RunningPort(service string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	port, exists := m.running[service]
	return port, exists
}

// List snapshots the whole registry (GET /api/services).
func (m *Manager) List() map[string]api.TaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]api.TaskInfo, len(m.tasks))
	for name, info := range m.tasks {
		out[name] = *info
	}
	return out
}

// Status snapshots one task; while it runs, live container logs are
// attached. Stored crash logs survive container removal.
func (m *Manager) Status(name string) (api.TaskInfo, bool) {
	m.mu.Lock()
	info, exists := m.tasks[name]
	var snapshot api.TaskInfo
	if exists {
		snapshot = *info
	}
	m.mu.Unlock()
	if !exists {
		return api.TaskInfo{}, false
	}
	if snapshot.Status == api.TaskStatusRunning {
		out, err := m.exec("logs", "--tail", fmt.Sprint(m.cfg.LogsTail), executor.ContainerName(name)).CombinedOutput()
		if err == nil {
			snapshot.Logs = string(out)
		} else {
			snapshot.Logs = fmt.Sprintf("Failed to get logs: %v - %s", err, string(out))
		}
	}
	return snapshot, true
}

// StartService starts the container for a deployed service. Fixed-port
// TCP services use their persisted host port; web services get a
// dynamically allocated one.
func (m *Manager) StartService(service string) (string, error) {
	containerName := executor.ContainerName(service)

	if _, exists := m.RunningPort(service); exists {
		return "", fmt.Errorf("service already running")
	}

	img, found := m.cfg.Images.Get(service)
	if !found {
		return "", fmt.Errorf("image not deployed on this runner: %s", service)
	}
	fixedPort := img.ServiceExternalPort > 0

	var externalPort int
	if fixedPort {
		externalPort = img.ServiceExternalPort
	} else {
		var err error
		externalPort, err = m.cfg.Ports.Next()
		if err != nil {
			return "", err
		}
	}
	internalPort := fmt.Sprint(img.ServiceInternalPort)

	if err := os.MkdirAll(m.extDir(containerName), 0o755); err != nil {
		return "", fmt.Errorf("can't create source directory to mount")
	}

	args := []string{
		"run",
		"-d",
		"--name", containerName,
		"--pull", "always",
		"--mount", "type=bind,src=" + m.extDir(containerName) + ",dst=/ext",
	}
	if mounts, err := executor.SymlinkTargets(m.extDir(containerName)); err == nil {
		for _, x := range mounts {
			args = append(args, "--mount", "type=bind,src="+x+",dst="+x)
		}
	}
	args = append(args,
		"-p", fmt.Sprintf("%d:%s", externalPort, internalPort),
		m.imageRef(service),
	)

	slog.Info("Starting service container",
		"service", containerName,
		"command", strings.Join(args, " "),
	)

	if output, err := m.exec(args...).CombinedOutput(); err != nil {
		// Return the port to the dynamic pool on failure. Fixed-port TCP
		// services keep their reserved port across restarts, so don't
		// release.
		if !fixedPort {
			m.cfg.Ports.Release(externalPort)
		}
		slog.Error("Failed to start service", "service", service, "error", err, "output", string(output))
		return "", fmt.Errorf("failed to start service: %s", string(output))
	}

	m.Activate(service, api.TaskTypeService, externalPort)
	go m.watchReadiness(service, containerName, externalPort, fixedPort)
	slog.Info("Service started", "service", service, "external_port", externalPort, "internal_port", internalPort)

	return fmt.Sprintf("Service %s started on port %d", service, externalPort), nil
}

// StopTask stops a running container and updates the registry.
func (m *Manager) StopTask(task string) (string, error) {
	containerName := executor.ContainerName(task)

	port, exists := m.RunningPort(task)
	if !exists {
		return "", fmt.Errorf("service not running")
	}

	m.Deactivate(task)

	if output, err := m.exec("stop", containerName).CombinedOutput(); err != nil {
		slog.Error("Failed to stop task", "task", task, "error", err, "output", string(output))
		return "", fmt.Errorf("failed to stop service: %s", string(output))
	}
	m.exec("rm", containerName).Run()

	// Release the port back to the dynamic pool, unless it is a fixed-port
	// TCP service whose port stays reserved across stop/start cycles.
	img, _ := m.cfg.Images.Get(task)
	if img.ServiceExternalPort == 0 {
		m.cfg.Ports.Release(port)
	}
	slog.Info("Task stopped", "task", task, "port", port)

	return fmt.Sprintf("Service %s stopped and port %d released", task, port), nil
}

// Restore reserves the fixed ports of deployed TCP services, registers
// every service task, and starts the autostart ones. Run it at startup,
// after the image registry has been loaded.
func (m *Manager) Restore() {
	for _, img := range m.cfg.Images.List() {
		if img.ServiceExternalPort > 0 {
			m.cfg.Ports.Reserve(img.ServiceExternalPort)
		}
	}
	for _, img := range m.cfg.Images.List() {
		if !img.IsService {
			continue
		}
		m.RegisterTask(img.Name, api.TaskTypeService)
		if img.ServiceAutostart {
			if _, err := m.StartService(img.Name); err != nil {
				slog.Error("Autostart failed", "service", img.Name, "error", err)
			}
		}
	}
}

// StopAll stops every running service (graceful shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.running))
	for name := range m.running {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		if _, err := m.StopTask(name); err != nil {
			slog.Warn("Failed to stop service on shutdown", "service", name, "error", err)
		}
	}
}

func (m *Manager) exec(args ...string) *exec.Cmd {
	return exec.Command(m.cfg.Bin, args...)
}

// containerState reports whether the container is up, with an exit
// reason when it is not. A failed inspect means the container is gone
// (removed) — treat as exited.
func (m *Manager) containerState(containerName string) (bool, string) {
	out, err := m.exec("inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", containerName).CombinedOutput()
	if err != nil {
		return false, "container gone: " + strings.TrimSpace(string(out))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) >= 1 && fields[0] == "true" {
		return true, ""
	}
	exit := "?"
	if len(fields) >= 2 {
		exit = fields[1]
	}
	return false, "exit code " + exit
}

// watchReadiness runs after StartService. Only a container that EXITED
// within the probe window is declared crashed; a container still up but
// slow to accept connections is left alone (no false positives for
// slow-boot apps).
func (m *Manager) watchReadiness(service, containerName string, externalPort int, fixedPort bool) {
	time.Sleep(m.cfg.ReadinessDelay)
	deadline := time.Now().Add(m.cfg.ReadinessTimeout)
	for {
		up, reason := m.containerState(containerName)
		if !up {
			m.markCrashed(service, containerName, externalPort, fixedPort, reason)
			return
		}
		if time.Now().After(deadline) {
			slog.Info("Service still starting after probe window", "service", service)
			return
		}
		time.Sleep(m.cfg.ReadinessInterval)
	}
}

// markCrashed captures the container's last log lines BEFORE removing it
// (services do not run with --rm), then flips the task state.
func (m *Manager) markCrashed(service, containerName string, externalPort int, fixedPort bool, reason string) {
	logs := ""
	if out, err := m.exec("logs", "--tail", "50", containerName).CombinedOutput(); err == nil {
		logs = string(out)
	} else {
		logs = fmt.Sprintf("failed to fetch logs: %v - %s", err, out)
	}
	m.exec("rm", containerName).Run()

	m.mu.Lock()
	delete(m.running, service)
	if info, ok := m.tasks[service]; ok {
		info.Status = api.TaskStatusCrashed
		info.ExternalPort = 0
		info.Logs = "service exited after start (" + reason + "):\n" + logs
	}
	m.mu.Unlock()

	if !fixedPort {
		m.cfg.Ports.Release(externalPort)
	}
	slog.Error("Service crashed after start", "service", service, "reason", reason)
}
