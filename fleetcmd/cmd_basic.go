// Subcommands: status, specs show, restart.
package fleetcmd

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	runnerapi "github.com/uidbz/tie/runner/api"
)

func runStatus(args []string) error {
	var cf commonFlags
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cf.register(fs)
	fs.Parse(args)

	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	hosts := cfg.Hosts
	// Registry auto-expand: active hosts that joined via the runner's
	// register mode but are not in [[Hosts]] yet show up without a config
	// edit. TOML entries win on name/URL collision.
	if cfg.Seed != "" {
		if cl, err := newAPI(cf.ca, cfg.AuthUser); err == nil {
			if tok, err := fleetAdminToken("", cfg.Hosts, cfg.Seed); err == nil {
				if reg, err := cl.fleetHosts(cfg.Seed, tok); err == nil {
					hosts = mergeRegistryHosts(hosts, reg)
				}
			}
		}
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}
	fail := false
	for _, h := range hosts {
		caps, err := cl.capabilities(h.API)
		if err != nil {
			fmt.Printf("%-10s DOWN (%v)\n", h.Name, err)
			fail = true
			continue
		}
		ver := caps.Version
		if ver == "" {
			ver = "?"
		}
		commit := caps.GitCommit
		if commit == "" {
			commit = "?"
		}
		fmt.Printf("%-10s v=%s commit=%s active=%d queue=%d caps=%s\n",
			h.Name, ver, commit, caps.ActiveJobs, caps.QueueLength, strings.Join(caps.Capabilities, ","))
	}
	if fail {
		os.Exit(1)
	}
	return nil
}

// mergeRegistryHosts appends active registry hosts missing from the TOML
// inventory (matched by API base or name).
func mergeRegistryHosts(hosts []Host, registry []runnerapi.FleetHost) []Host {
	seenAPI := map[string]bool{}
	seenName := map[string]bool{}
	for _, h := range hosts {
		seenAPI[h.API] = true
		seenName[h.Name] = true
	}
	for _, r := range registry {
		if r.Status != runnerapi.HostActive {
			continue
		}
		api := strings.TrimRight(r.URL, "/")
		if seenAPI[api] || seenName[r.Name] {
			continue
		}
		hosts = append(hosts, Host{Name: r.Name, API: api})
	}
	return hosts
}

func runRestart(args []string) error {
	var cf commonFlags
	var only string
	var allowSelf bool
	var idleTimeout time.Duration
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&only, "only", "", "restrict to one host")
	fs.BoolVar(&allowSelf, "allow-self", false, "permit restarting the host this tool runs on")
	fs.DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "how long to wait for the host to go idle")
	fs.Parse(args)

	cfg, err := loadFleetConfig(cf.conf)
	if err != nil {
		return err
	}
	hosts := filterHosts(cfg.Hosts, only)
	if len(hosts) == 0 {
		return fmt.Errorf("no fleet host matches --only %q", only)
	}
	cl, err := newAPI(cf.ca, cfg.AuthUser)
	if err != nil {
		return err
	}
	ssh := sshRunner{user: cf.sshUser}
	for _, h := range hosts {
		if err := guardSelf(h, allowSelf); err != nil {
			return err
		}
		if err := idleGate(cl, h, idleTimeout); err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
		if err := warnNoAutostart(cl, ssh, h, cfg.RemoteConfig); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: autostart pre-flight failed: %v\n", h.Name, err)
		}
		fmt.Printf("restarting %s ...\n", h.Name)
		out, err := ssh.run(h.Name, serviceCmd(h.Init, "restart", cfg.Rollout.ServiceName, cfg.Rollout.PidFile))
		if err != nil {
			return fmt.Errorf("%s: restart: %w", h.Name, err)
		}
		if out != "" {
			fmt.Println(out)
		}
	}
	return nil
}

// idleGate blocks until the runner reports no active jobs and an empty queue,
// or the timeout expires. Never restart a runner under load.
func idleGate(cl *api, h Host, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		caps, err := cl.capabilities(h.API)
		if err != nil {
			return fmt.Errorf("API unreachable, refusing to restart blind: %w", err)
		}
		if caps.ActiveJobs == 0 && caps.QueueLength == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still busy after %v (active=%d queue=%d)", timeout, caps.ActiveJobs, caps.QueueLength)
		}
		fmt.Printf("  %s busy (active=%d queue=%d), waiting...\n", h.Name, caps.ActiveJobs, caps.QueueLength)
		time.Sleep(30 * time.Second)
	}
}

// warnNoAutostart prints running services that will NOT come back after a
// restart. Runners that adopt running containers at boot make this moot for
// services that were up before the restart; on older ones only
// ServiceAutostart=true services survive, and those already down stay down.
func warnNoAutostart(cl *api, ssh sshRunner, h Host, remoteConfig string) error {
	if caps, err := cl.capabilities(h.API); err == nil && supportsAdoption(caps) {
		fmt.Printf("  %s: runner %s adopts running services across restarts; autostart only re-starts already-stopped ones\n", h.Name, caps.Version)
		return nil
	}
	remote, err := ssh.run(h.Name, "cat ~/"+remoteConfig)
	if err != nil {
		return err
	}
	off := NoAutostart(remote)
	running, err := cl.runningServices(h.API)
	if err != nil {
		return err
	}
	offSet := map[string]bool{}
	for _, n := range off {
		offSet[n] = true
	}
	var danger []string
	for _, n := range running {
		if offSet[n] {
			danger = append(danger, n)
		}
	}
	if len(danger) > 0 {
		fmt.Printf("  WARNING: running services WITHOUT autostart (will stay down after restart): %s\n", strings.Join(danger, ", "))
	}
	return nil
}
