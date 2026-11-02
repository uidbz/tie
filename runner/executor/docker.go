package executor

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Docker is the local runtime: it runs each job as a `docker run` of the
// image named by job.Handler, invoking the entrypoint binary (ExecName)
// with the frozen CLI contract. The container fetches its input from the
// filehost by hash and reports through the marker protocol.
type Docker struct {
	// Bin is the container CLI on PATH ("docker"). It is configurable so
	// deployments can point at a wrapper or an alternative runtime, and
	// so tests can fake execution without a daemon.
	Bin string
	// Registry prefixes the image name (empty means handler is the full
	// image reference).
	Registry string
	// Filehost and Store are passed through to the entrypoint binary.
	Filehost string
	Store    string
	// ExtDir is the host-side scratch base, bind-mounted into the
	// container at /ext (default "/tie"). Symlinks placed in a job's
	// directory by the filehost layer are mounted through at their own
	// paths, so container-side paths stay host-faithful.
	ExtDir string
	// Container images carry the entrypoint binary; its CLI contract is
	// fixed (see runArgs), so the runtime never overrides the entrypoint.
	// DefaultMemMB caps container memory when the job-type spec carries
	// none; 0 there too means no cap.
	DefaultMemMB int
	// Protocol is the accepted marker protocol.
	Protocol Protocol
}

func (d *Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

func (d *Docker) extDir() string {
	if d.ExtDir == "" {
		return "/tie"
	}
	return d.ExtDir
}

// ContainerName derives the docker container name from the image handler.
// Per-handler serialization guarantees at most one running container per
// handler, so the handler-derived name is unambiguous — Stop relies on
// it, as does the task registry.
func ContainerName(handler string) string {
	safe := strings.ReplaceAll(handler, "/", "-")
	safe = strings.ReplaceAll(safe, ":", "-")
	safe = strings.ReplaceAll(safe, ".", "-")
	return safe
}

// imageRef resolves the image reference for a handler.
func (d *Docker) imageRef(handler string) string {
	if d.Registry == "" {
		return handler
	}
	return strings.TrimSuffix(d.Registry, "/") + "/" + handler
}

// runArgs builds the full argument vector for the container CLI. The
// order is stable on purpose: it is what the debug log and tests compare
// against, and the trailing entrypoint flags are the container-side
// contract.
func (d *Docker) runArgs(job *api.JobStatus, spec Spec, containerName, settingsPath string, symlinkMounts []string) []string {
	extPath := d.extDir() + "/" + containerName
	args := []string{
		"run",
		"--rm",
		"--name", containerName,
		"--mount", "type=bind,src=" + extPath + ",dst=/ext",
	}
	for _, x := range symlinkMounts {
		args = append(args, "--mount", "type=bind,src="+x+",dst="+x)
	}
	if settingsPath != "" {
		args = append(args, "-v", settingsPath+":/settings.json")
	}
	if job.IsProd {
		args = append(args, "--pull", "always")
	}
	// Resource limits come from the job type's spec (sizing is job-type driven).
	if spec.CPU > 0 {
		args = append(args, "--cpus="+strconv.Itoa(spec.CPU))
	}
	if spec.GPU > 0 {
		args = append(args, "--gpus", "all")
	}
	// Cap container memory so a leaking job is OOM-killed in isolation
	// (exit 137) rather than exhausting the host. The spec's RAMMB is
	// authoritative; if it's 0 we fall back to the configured default;
	// 0 there too means no cap. --memory-swap equal to --memory disables
	// swap so the process can't dodge it.
	if memMB := spec.RAMMB; memMB > 0 || d.DefaultMemMB > 0 {
		if memMB == 0 {
			memMB = d.DefaultMemMB
		}
		limit := strconv.Itoa(memMB) + "m"
		args = append(args, "--memory="+limit, "--memory-swap="+limit)
	}
	// Image + entrypoint args (the frozen CLI contract).
	args = append(args, d.imageRef(job.Handler),
		"-filehost", d.Filehost,
		"-inputhash", job.InputHash,
		"-debug="+strconv.FormatBool(job.IsDebug),
	)
	if d.Store != "" {
		args = append(args, "-store", d.Store)
	}
	if settingsPath != "" {
		args = append(args, "-settings", "/settings.json")
	}
	if job.AppHash != "" {
		args = append(args, "-apphash", job.AppHash)
	}
	return args
}

// SymlinkTargets returns the target path (as stored in the symlink) for
// each symlink directly inside dir (non-recursive). The filehost layer
// drops symlinks in a job's or service's directory pointing at large
// blobs on the host; the container must see those paths, so each target
// is bind-mounted at its own path.
func SymlinkTargets(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, e := range entries {
		// Use Lstat (via entry.Info) to avoid following the link
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		linkPath := filepath.Join(dir, e.Name())
		tgt, err := os.Readlink(linkPath)
		if err != nil {
			return nil, err
		}
		targets = append(targets, tgt)
	}
	return targets, nil
}

// Run executes the job to container exit. The caller marks the job as
// running (status, started time) before calling and finalizes after.
func (d *Docker) Run(ctx context.Context, job *api.JobStatus, opts RunOptions) ExitInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	containerName := ContainerName(job.Handler)

	if opts.Hooks.OnStart != nil {
		opts.Hooks.OnStart(containerName)
	}
	defer func() {
		if opts.Hooks.OnEnd != nil {
			opts.Hooks.OnEnd(containerName)
		}
	}()

	extPath := d.extDir() + "/" + containerName
	if err := os.MkdirAll(extPath, 0o755); err != nil {
		opts.Hooks.err(job, "failed to create job directory: "+err.Error())
		slog.Error("Failed to create job directory", "container", containerName, "error", err)
		return ExitInfo{ExitCode: 1}
	}

	var mounts []string
	if m, err := SymlinkTargets(extPath); err == nil {
		mounts = m
	}

	settingsPath := ""
	if len(opts.Settings) > 0 {
		file, err := os.CreateTemp("", "runner-settings-*")
		if err != nil {
			opts.Hooks.err(job, err.Error())
			slog.Error("Failed to create temp settings file", "handler", job.Handler, "error", err)
			return ExitInfo{ExitCode: 1}
		}
		if _, err := file.Write(opts.Settings); err != nil {
			file.Close()
			os.Remove(file.Name())
			opts.Hooks.err(job, err.Error())
			slog.Error("Failed to write settings file", "handler", job.Handler, "error", err)
			return ExitInfo{ExitCode: 1}
		}
		file.Close()
		settingsPath = file.Name()
		defer os.Remove(settingsPath)
	}

	args := d.runArgs(job, opts.Spec, containerName, settingsPath, mounts)
	slog.Info("Starting container job",
		"handler", job.Handler,
		"input_hash", job.InputHash,
		"job_type", job.JobType,
		"is_prod", job.IsProd,
		"is_debug", job.IsDebug,
		"command", strings.Join(args, " "),
	)

	cmd := exec.CommandContext(ctx, d.bin(), args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		opts.Hooks.err(job, "Error creating StdoutPipe: "+err.Error())
		slog.Error("Failed to create stdout pipe", "handler", job.Handler, "error", err)
		return ExitInfo{ExitCode: 1}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		opts.Hooks.err(job, "Error creating StderrPipe: "+err.Error())
		slog.Error("Failed to create stderr pipe", "handler", job.Handler, "error", err)
		return ExitInfo{ExitCode: 1}
	}

	if err := cmd.Start(); err != nil {
		opts.Hooks.err(job, "Error starting command: "+err.Error())
		slog.Error("Failed to start container command", "handler", job.Handler, "error", err)
		return ExitInfo{ExitCode: 1}
	}

	// Sample the container's memory usage until the process exits,
	// recording the current and peak resident size (MB) so status and
	// history can show consumption.
	sampleDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sampleDone:
				return
			case <-ticker.C:
				if mb, ok := d.memoryMB(containerName); ok {
					opts.Hooks.mem(job, mb)
				}
			}
		}
	}()

	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			d.Protocol.Apply(job, scanner.Text(), opts.Hooks)
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			opts.Hooks.log(job, scanner.Text())
		}
	}()

	// Inspect the container's exit code and OOM flag before --rm removes
	// it; the container record still exists briefly after Wait returns.
	waitErr := cmd.Wait()
	close(sampleDone)
	info := d.inspectExit(containerName)
	if waitErr != nil {
		var cancelled bool
		if ctx.Err() != nil {
			cancelled = true
		}
		if ee, ok := waitErr.(*exec.ExitError); ok && !cancelled {
			info.ExitCode = ee.ExitCode()
		}
		if !cancelled {
			opts.Hooks.err(job, "Error waiting for command: "+waitErr.Error())
		}
		slog.Error("Container command wait failed", "handler", job.Handler, "error", waitErr, "exit_code", info.ExitCode, "oom_killed", info.OOMKilled)
	}
	// Ensure stdout (which parses SetResult/PercentDone) is fully drained
	// before the caller finalizes and persists the terminal state.
	<-stdoutDone
	return info
}

// Stop asks a running container to terminate (user cancellation).
func (d *Docker) Stop(containerName string) error {
	out, err := exec.Command(d.bin(), "stop", containerName).CombinedOutput()
	if err != nil {
		return err
	}
	_ = out
	return nil
}

// memoryMB returns the container's current memory usage in MB via
// `docker stats`. Returns (0, false) if the container isn't running or
// the output can't be parsed (e.g. it exited between samples).
func (d *Docker) memoryMB(containerName string) (int64, bool) {
	out, err := exec.Command(d.bin(), "stats", "--no-stream", "--format", "{{.MemUsage}}", containerName).Output()
	if err != nil {
		return 0, false
	}
	// Format is like "1.234GiB / 8GiB"; take the used portion before the slash.
	used, _, ok := strings.Cut(strings.TrimSpace(string(out)), "/")
	if !ok {
		return 0, false
	}
	mb, ok := parseDockerSizeMB(strings.TrimSpace(used))
	return mb, ok
}

// parseDockerSizeMB converts a docker byte size (e.g. "1.234GiB",
// "512MiB", "900KiB", "10B") into whole MB.
func parseDockerSizeMB(s string) (int64, bool) {
	units := []struct {
		suffix string
		bytes  float64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, false
			}
			return int64(v * u.bytes / (1 << 20)), true
		}
	}
	return 0, false
}

// inspectExit reads the container's OOMKilled flag and exit code before
// --rm removes it. Returns zeros if the container is already gone.
func (d *Docker) inspectExit(containerName string) ExitInfo {
	out, err := exec.Command(d.bin(), "inspect", "--format", "{{.State.OOMKilled}} {{.State.ExitCode}}", containerName).Output()
	if err != nil {
		return ExitInfo{}
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return ExitInfo{}
	}
	code, _ := strconv.Atoi(fields[1])
	return ExitInfo{ExitCode: code, OOMKilled: fields[0] == "true"}
}
