package scheduler

import (
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ResolveCapacity fills zero totals with what this host actually has:
// CPU from Go's scheduler view, RAM from /proc/meminfo, GPUs by counting
// `nvidia-smi -L` lines (0 when the binary is absent). Configured totals
// always win — set them to reserve headroom for the OS or co-tenants.
func ResolveCapacity(totalCPU, totalRAMMB, totalGPU int) (cpu, ramMB, gpu int) {
	cpu = totalCPU
	if cpu == 0 {
		cpu = runtime.NumCPU()
	}
	ramMB = totalRAMMB
	if ramMB == 0 {
		ramMB = detectHostRAMMB()
	}
	gpu = totalGPU
	if gpu == 0 {
		gpu = detectHostGPU()
	}
	slog.Info("Resolved scheduler capacity", "total_cpu", cpu, "total_ram_mb", ramMB, "total_gpu", gpu)
	return cpu, ramMB, gpu
}

// detectHostRAMMB reads MemTotal from /proc/meminfo (in kB) and returns
// MB. Returns 0 when it cannot be read or parsed.
func detectHostRAMMB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line) // e.g. ["MemTotal:", "32817840", "kB"]
		if len(fields) >= 2 {
			if kb, err := strconv.Atoi(fields[1]); err == nil {
				return kb / 1024
			}
		}
	}
	return 0
}

// detectHostGPU counts the GPUs reported by `nvidia-smi -L`. Returns 0 if
// the binary is missing or errors (hosts without NVIDIA GPUs).
func detectHostGPU() int {
	out, err := exec.Command(nvidiaSMIBin, "-L").Output()
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// nvidiaSMIBin is a var so tests (and unusual installs) can point it
// elsewhere.
var nvidiaSMIBin = "nvidia-smi"

// StartCleanUp runs CleanUp on an interval until Stop (or the process
// exits). The durable store trims independently by retention policy.
func (m *Manager) StartCleanUp(interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				m.CleanUp()
			}
		}
	}()
}
