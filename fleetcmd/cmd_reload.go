// reload: apply the on-disk runner config without restarting the runner
// (hot config reload). Per host: try POST /api/reload (Basic Auth: the
// configured auth user, password = AdminToken), else fall back to
// systemctl reload over SSH. Reload never touches service containers, so
// no idle-gate or autostart warning is needed.
//
// AdminToken resolution: --token flag > TIE_FLEET_ADMIN_TOKEN env > the
// optional `token` key on the [[Hosts]] entry in the fleet config.
package fleetcmd

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	runnerapi "github.com/uidbz/tie/runner/api"
)

func runReload(args []string) error {
	var cf commonFlags
	var only, token string
	var forceRestart bool
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	cf.register(fs)
	fs.StringVar(&only, "only", "", "restrict to one host")
	fs.StringVar(&token, "token", "", bt("AdminToken for POST /api/reload (else TIE_FLEET_ADMIN_TOKEN env, else the [[Hosts]] token key)"))
	fs.BoolVar(&forceRestart, "restart", false, "skip HTTP and use sudo systemctl restart (kills service containers on pre-v1.6.0 runners; the pre-v1.2.0 escape hatch)")
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
	envToken := os.Getenv(adminTokenEnv())

	for _, h := range hosts {
		caps, err := cl.capabilities(h.API)
		if err != nil {
			return fmt.Errorf("%s: API unreachable, refusing to reload blind: %w", h.Name, err)
		}
		if !supportsReload(caps) {
			return fmt.Errorf(bt("%s: build %s/%s predates hot config reload — SIGHUP would KILL the runner and its service containers; use 'tie-fleet restart --only %s' or roll it out first"), h.Name, caps.Version, caps.GitCommit, h.Name)
		}
		if forceRestart {
			fmt.Printf("%s: forced restart ...\n", h.Name)
			if _, err := ssh.run(h.Name, serviceCmd(h.Init, "restart", cfg.Rollout.ServiceName, cfg.Rollout.PidFile)); err != nil {
				return fmt.Errorf("%s: restart: %w", h.Name, err)
			}
			continue
		}
		tok := token
		if tok == "" {
			tok = envToken
		}
		if tok == "" {
			tok = h.Token
		}
		if tok != "" {
			msg, err := cl.reload(h.API, tok)
			if err == nil {
				fmt.Printf("%s: reloaded over HTTP (%s)\n", h.Name, msg)
				continue
			}
			fmt.Printf("%s: HTTP reload failed: %v\n", h.Name, err)
		}
		// SSH fallback: systemctl reload sends SIGHUP, safe on a6a6acf+
		// (checked above). Needs a sudoers rule for `systemctl reload`.
		out, err := ssh.run(h.Name, serviceCmd(h.Init, "reload", cfg.Rollout.ServiceName, cfg.Rollout.PidFile))
		if err != nil {
			return fmt.Errorf(bt("%s: service reload failed (%v)\n  add a NOPASSWD sudoers rule for '%s' on %s,\n  or set AdminToken in the runner config and pass --token / TIE_FLEET_ADMIN_TOKEN for HTTP reload"),
				h.Name, strings.TrimSpace(out),
				strings.TrimPrefix(serviceCmd(h.Init, "reload", cfg.Rollout.ServiceName, cfg.Rollout.PidFile), "sudo "), h.Name)
		}
		fmt.Printf("%s: reloaded via systemctl (SIGHUP)\n", h.Name)
	}
	return nil
}

// parseVersion extracts MAJOR.MINOR from an ldflags-stamped "vN.M..." version
// string. Short commit SHAs and plain branch names do not parse.
func parseVersion(v string) (major, minor int, ok bool) {
	if len(v) < 4 || v[0] != 'v' || v[1] < '0' || v[1] > '9' {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(v, "v%d.%d", &major, &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// supportsReload reports whether the deployed build has hot config reload
// (SIGHUP handler). The early 1.0/1.1-line deployed builds die on SIGHUP and
// take every service container with them; every later line (1.2+ and the 0.x
// line and up) has the handler. Unversioned dev builds are treated as not
// having it.
func supportsReload(caps runnerapi.CapabilityReply) bool {
	major, minor, ok := parseVersion(caps.Version)
	return ok && !(major == 1 && minor < 2)
}

// supportsAdoption reports whether the deployed runner keeps service
// containers across restarts: SIGTERM leaves them running and boot
// re-registers them instead of recreating. Deployed builds of the 1.0 to 1.5
// line recreate; 1.6+ and the 0.x line and up adopt. Gated on Version like
// supportsReload.
func supportsAdoption(caps runnerapi.CapabilityReply) bool {
	major, minor, ok := parseVersion(caps.Version)
	return ok && !(major == 1 && minor < 6)
}

// reload POSTs /api/reload with Basic Auth (the configured auth user, the AdminToken).
func (cl *api) reload(apiBase, token string) (string, error) {
	req, err := http.NewRequest(http.MethodPost, apiBase+"/api/reload", bytes.NewReader(nil))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(cl.user, token)
	resp, err := cl.c.Do(req) // client timeout (15s) is fine for a local op
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}
