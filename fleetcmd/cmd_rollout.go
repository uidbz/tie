// rollout: build once on the configured build host, then per fleet host:
// idle-gate -> skip if current -> pre-flight autostart warning -> stage
// .new + sha256 verify -> .prev backup -> atomic mv -> setcap -> restart ->
// health-check (API reports the new commit) -> auto-rollback on failure.
// The whole rollout stops on the first failed host (no cascading).
//
// BuildHost and RepoURL come from the fleet config's [Rollout] table:
// where the build happens and what gets built are deployment decisions.
package fleetcmd

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	buildDir    = "~/src/runner-rollout-build" // checkout on BuildHost
	stagedBin   = "/tmp/runner-rollout-bin"    // build output on BuildHost
	buildScript = `set -e
if [ ! -d ` + buildDir + ` ]; then git clone -q %s ` + buildDir + `; fi
cd ` + buildDir + `
git fetch -q origin
git checkout -q '%s' 2>/dev/null || git checkout -q 'origin/%s'
git pull -q --ff-only 2>/dev/null || true
COMMIT=$(git rev-parse --short HEAD)
go build -trimpath -ldflags "-X main.Version=%s -X main.GitCommit=$COMMIT -X main.BuildDate=$(date -u +%%FT%%TZ)" -o ` + stagedBin + ` .
echo BUILT $COMMIT`
)

func runRollout(args []string) error {
	var cf commonFlags
	var rev, only string
	var dryRun, allowSelf bool
	var idleTimeout time.Duration
	fs := flag.NewFlagSet("rollout", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&rev, "rev", "master", "tag, branch, or sha to deploy")
	fs.StringVar(&only, "only", "", "restrict to one host")
	fs.BoolVar(&dryRun, "dry-run", false, "gate and compare, change nothing")
	fs.BoolVar(&allowSelf, "allow-self", false, "permit touching the host this tool runs on")
	fs.DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "how long to wait for a host to go idle")
	fs.Parse(args)

	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	if cfg.Rollout.BuildHost == "" || cfg.Rollout.RepoURL == "" {
		return fmt.Errorf("rollout needs [Rollout] BuildHost and RepoURL in the fleet config")
	}
	target := filterHosts(cfg.Hosts, only)
	if len(target) == 0 {
		return fmt.Errorf("no fleet host matches --only %q", only)
	}
	for _, h := range target {
		if err := guardSelf(h, allowSelf); err != nil {
			return err
		}
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}
	ssh := sshRunner{user: cf.sshUser}
	remoteBin := cfg.Rollout.RemoteBin
	svc := serviceCmd

	// ---- build once ----
	fmt.Printf("building %s on %s ...\n", rev, cfg.Rollout.BuildHost)
	buildOut, err := ssh.run(cfg.Rollout.BuildHost, fmt.Sprintf(buildScript, cfg.Rollout.RepoURL, rev, rev, rev))
	if err != nil {
		return fmt.Errorf("build failed on %s: %w", cfg.Rollout.BuildHost, err)
	}
	_ = buildOut
	commit, err := ssh.run(cfg.Rollout.BuildHost, "cd "+buildDir+" && git rev-parse --short HEAD")
	if err != nil {
		return err
	}
	fmt.Printf("built %s -> commit %s\n", rev, commit)

	// pull the built binary local for hashing and redistribution
	localBin, err := os.CreateTemp("", "runner-rollout-bin")
	if err != nil {
		return err
	}
	defer os.Remove(localBin.Name())
	localBin.Close()
	if err := ssh.copyFrom(cfg.Rollout.BuildHost, stagedBin, localBin.Name()); err != nil {
		return err
	}
	sha, err := sha256File(localBin.Name())
	if err != nil {
		return err
	}

	// ---- per host ----
	for _, h := range target {
		fmt.Printf("=== %s ===\n", h.Name)
		if err := idleGate(cl, h, idleTimeout); err != nil {
			fmt.Printf("  SKIP %s: %v\n", h.Name, err)
			os.Exit(1)
		}
		caps, err := cl.capabilities(h.API)
		if err != nil {
			fmt.Printf("  SKIP %s: %v\n", h.Name, err)
			os.Exit(1)
		}
		if caps.GitCommit == commit {
			fmt.Printf("  up to date (%s), skipping\n", commit)
			continue
		}
		fmt.Printf("  %s -> %s\n", caps.GitCommit, commit)
		if dryRun {
			fmt.Println("  dry-run: stopping here")
			continue
		}
		if err := warnNoAutostart(cl, ssh, h, cfg.RemoteConfig); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: autostart pre-flight failed: %v\n", h.Name, err)
		}
		if err := swapAndRestart(ssh, h, localBin.Name(), sha, cfg.Rollout); err != nil {
			fmt.Printf("  FAILED: %v\n", err)
			os.Exit(1)
		}
		if !healthCheck(cl, h, commit) {
			fmt.Printf("  FAILED health check — rolling back %s\n", h.Name)
			// A restored binary that binds a privileged port must setcap
			// after the mv — without it the rollback crash-loops on
			// bind: permission denied.
			rb := fmt.Sprintf("set -e; test -f %s.prev; mv -f %s.prev %s; sudo setcap cap_net_bind_service+ep %s; %s",
				remoteBin, remoteBin, remoteBin, remoteBin, svc(h.Init, "restart", cfg.Rollout.ServiceName, cfg.Rollout.PidFile))
			if _, err := ssh.run(h.Name, rb); err != nil {
				fmt.Printf("  ROLLBACK ALSO FAILED, manual intervention needed: %v\n", err)
			} else {
				fmt.Printf("  rollback issued; verify: curl %s/api/capabilities\n", h.API)
			}
			os.Exit(1) // stop the rollout; do not cascade
		}
		fmt.Printf("  OK: %s now at %s\n", h.Name, commit)
	}
	return nil
}

// swapAndRestart stages the binary as .new, verifies its sha256 on the host,
// backs up the running binary as .prev, then atomically mv + chmod + setcap
// + restart. mv-over-running is safe (the live process keeps the old inode);
// setcap runs on the final path only, matching the sudoers rule exactly.
func swapAndRestart(ssh sshRunner, h Host, localBin, sha string, r Rollout) error {
	if _, err := ssh.run(h.Name, fmt.Sprintf("test -x %s && cp -a %s %s.prev", r.RemoteBin, r.RemoteBin, r.RemoteBin)); err != nil {
		return fmt.Errorf("backup .prev: %w", err)
	}
	if err := ssh.copyTo(localBin, h.Name, r.RemoteBin+".new"); err != nil {
		return err
	}
	// scp does not reliably preserve the exec bit: the staged binary can
	// land 0600, mv keeps it, and the init system fails 203/EXEC. chmod +x
	// explicitly after the mv, before setcap and the exec probe.
	script := fmt.Sprintf("set -e; echo '%s  %s.new' | sha256sum -c - >/dev/null; mv -f %s.new %s; chmod +x %s; sudo setcap cap_net_bind_service+ep %s; %s --version >/dev/null; %s",
		sha, r.RemoteBin, r.RemoteBin, r.RemoteBin, r.RemoteBin, r.RemoteBin, r.RemoteBin,
		serviceCmd(h.Init, "restart", r.ServiceName, r.PidFile))
	if _, err := ssh.run(h.Name, script); err != nil {
		return fmt.Errorf("swap/restart (if setcap failed, the binary is swapped but not live; %s.prev is the old one): %w", r.RemoteBin, err)
	}
	return nil
}

// healthCheck polls /api/capabilities for up to ~30s until the runner answers
// and reports the target commit.
func healthCheck(cl *api, h Host, commit string) bool {
	for i := 0; i < 10; i++ {
		time.Sleep(3 * time.Second)
		caps, err := cl.capabilities(h.API)
		if err == nil && caps.GitCommit == commit {
			return true
		}
	}
	return false
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
