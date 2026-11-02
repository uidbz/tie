package main

// Hot config reload: re-read tie-runner-config.toml and swap the
// runtime-mutable fields without restarting the server. Triggered by
// SIGHUP or POST /api/reload (gated on AdminToken, see runner/server).
// The file is parsed and validated into a throwaway ServerConfig first,
// so a broken edit can never take down a running runner: on any error
// the live config is left untouched.

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"syscall"

	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/scheduler"

	"github.com/uidbz/conf"
)

// reloadConfig re-reads the config file and atomically swaps the
// hot-swappable fields into the live config. Startup-only fields
// (Address, TLS paths, JobDBPath, FileServer) are never applied; if they
// changed, a warning names them so the operator knows a restart is still
// required.
//
// DeployedImages is deliberately off-limits: the runner itself writes the
// file on every deploy, so the image list in the file is a runner-managed
// artifact, not something a reload should overwrite.
func reloadConfig() error {
	fresh := defaultServerConfig()
	if _, err := conf.LoadFromCurrentDir(configFileName, &fresh); err != nil {
		return fmt.Errorf("config parse failed, keeping live config: %w", err)
	}

	warnings := validateConfig(&fresh)

	// Report (but never apply) changes to startup-only fields.
	config.Lock()
	if config.Address != fresh.Address {
		slog.Warn("Config reload: Address changed but requires restart", "live", config.Address, "file", fresh.Address)
	}
	if config.TLSCert != fresh.TLSCert || config.TLSKey != fresh.TLSKey {
		slog.Warn("Config reload: TLSCert/TLSKey changed but require restart")
	}
	if config.JobDBPath != fresh.JobDBPath {
		slog.Warn("Config reload: JobDBPath changed but requires restart", "live", config.JobDBPath, "file", fresh.JobDBPath)
	}
	if !reflect.DeepEqual(config.FileServer, fresh.FileServer) {
		slog.Warn("Config reload: FileServer section changed but requires restart")
	}
	if !reflect.DeepEqual(config.DeployedImages, fresh.DeployedImages) {
		slog.Warn("Config reload: DeployedImages in file differ from live state; live state kept (the runner owns this section)")
	}

	oldCaps := slices.Clone(config.Capabilities)
	oldBackends := slices.Clone(config.Backends)

	// Swap the hot-swappable fields. Every reader takes config.Lock() or
	// reads through an accessor per request, so the swap is atomic for
	// them.
	config.Capabilities = fresh.Capabilities
	config.Backends = fresh.Backends
	config.Store = fresh.Store
	config.DockerRegistry = fresh.DockerRegistry
	config.Filehost = fresh.Filehost
	config.BuildArgs = fresh.BuildArgs
	config.DebugLogging = fresh.DebugLogging
	config.DefaultJobMemoryMB = fresh.DefaultJobMemoryMB
	config.AuthUser = fresh.AuthUser
	config.AdminToken = fresh.AdminToken
	config.FleetJoinToken = fresh.FleetJoinToken
	config.FleetSeed = fresh.FleetSeed
	config.ExtDir = fresh.ExtDir
	config.TotalCPU, config.TotalRAMMB, config.TotalGPU = fresh.TotalCPU, fresh.TotalRAMMB, fresh.TotalGPU
	config.JobTypeSpecs = fresh.JobTypeSpecs
	config.Unlock()

	applyDebugLogging(config.DebugLogging)

	// Re-resolve the scheduler pool against the new totals. Shrinking
	// below what running jobs already reserved is rejected (accounting
	// would go negative); the old totals stay until those jobs finish.
	// SetCapacity wakes the admission loop when the pool grew.
	if jobman != nil {
		cpu, ramMB, gpu := scheduler.ResolveCapacity(cfgPool())
		if !jobman.SetCapacity(cpu, ramMB, gpu) {
			snap := jobman.Snapshot()
			slog.Warn("Config reload: new capacity is below resources already in use; keeping current pool until running jobs finish",
				"requested_cpu", cpu, "requested_ram_mb", ramMB, "requested_gpu", gpu,
				"used_cpu", snap.TotalCPU-snap.FreeCPU, "used_ram_mb", snap.TotalRAMMB-snap.FreeRAMMB, "used_gpu", snap.TotalGPU-snap.FreeGPU)
		}
	}

	for _, w := range warnings {
		slog.Warn("Config reload warning", "warning", w)
	}
	slog.Info("Config reloaded",
		"capabilities", strings.Join(oldCaps, ",")+" -> "+strings.Join(config.Capabilities, ","),
		"backends", strings.Join(oldBackends, ",")+" -> "+strings.Join(config.Backends, ","),
		"total_cpu", config.TotalCPU, "total_ram_mb", config.TotalRAMMB, "total_gpu", config.TotalGPU,
		"warnings", len(warnings))
	return nil
}

// validateConfig returns warnings for a freshly loaded config. It catches
// the misconfiguration classes that produce confusing runtime errors:
//   - a capability advertised with no resource spec (jobs of it would
//     fail with "Unknown job type"),
//   - a spec that can never fit the runner's own pool,
//   - duplicate or empty backend entries,
//   - an enabled file server with an unreadable directory.
func validateConfig(fresh *ServerConfig) []string {
	var warnings []string

	// Job types the runner hosts without a local container spec.
	serviceTypes := []string{"WebserviceV1", "TCPServiceV1", "pipeline"}
	for _, cap := range fresh.Capabilities {
		if slices.Contains(serviceTypes, cap) {
			continue
		}
		if _, ok := executor.SpecFor(cap, fresh.JobTypeSpecs); !ok {
			warnings = append(warnings, fmt.Sprintf(
				"capability %q is advertised but has no resource spec: add [JobTypeSpecs.%s] or remove it from Capabilities (jobs of this type will fail with 'Unknown job type')", cap, cap))
		}
	}

	cpu, ramMB, gpu := scheduler.ResolveCapacity(fresh.TotalCPU, fresh.TotalRAMMB, fresh.TotalGPU)
	for jobType, spec := range fresh.JobTypeSpecs {
		if spec.CPU > cpu || spec.RAMMB > ramMB || spec.GPU > gpu {
			warnings = append(warnings, fmt.Sprintf(
				"JobTypeSpecs.%s (%d cores / %d MB / %d GPU) exceeds the runner pool (%d / %d / %d): jobs of this type can never be admitted",
				jobType, spec.CPU, spec.RAMMB, spec.GPU, cpu, ramMB, gpu))
		}
	}

	seen := map[string]bool{}
	for _, b := range fresh.Backends {
		if b == "" {
			warnings = append(warnings, "Backends contains an empty entry")
			continue
		}
		if seen[b] {
			warnings = append(warnings, fmt.Sprintf("Backends lists %q more than once", b))
		}
		seen[b] = true
	}

	if fresh.FileServer.Enabled {
		if fresh.FileServer.Dir == "" {
			warnings = append(warnings, "FileServer.Enabled but Dir is empty: file serving stays off")
		} else if _, err := os.Stat(fresh.FileServer.Dir); err != nil {
			warnings = append(warnings, fmt.Sprintf("FileServer.Dir %q is not readable: file requests will 404 (%v)", fresh.FileServer.Dir, err))
		}
	}
	return warnings
}

// startConfigReloadSignal wires SIGHUP to reloadConfig, the standard Unix
// habit for "re-read your config".
func startConfigReloadSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		for range ch {
			if err := reloadConfig(); err != nil {
				slog.Error("Config reload failed", "error", err)
			}
		}
	}()
}

// applyDebugLogging re-levels the process logger when DebugLogging
// changes at reload time (startup applies the same setting in main).
func applyDebugLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	if debug {
		slog.Info("Debug logging enabled")
	}
}
