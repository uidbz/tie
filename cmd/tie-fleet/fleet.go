// Fleet inventory and the runner API client. The inventory is a TOML config
// loaded via github.com/uidbz/conf: `tie-fleet init` writes tie-fleet.toml
// to the user config dir (os.UserConfigDir, e.g. ~/.config/tie-fleet/), and
// every command finds it again via conf.LoadConfig, which searches the
// current dir, then the user config dir, then /etc/tie-fleet. --conf <path>
// overrides with an explicit file.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/uidbz/conf"
	runnerapi "github.com/uidbz/tie/runner/api"
)

const (
	appName    = "tie-fleet"
	configFile = "tie-fleet.toml"

	defaultAuthUser     = "tie"
	defaultRemoteConfig = "tie-runner-config.toml"
	defaultRemoteBin    = "/usr/local/bin/tie-runner"
	defaultServiceName  = "tie-runner"
	defaultPidFile      = "/run/tie-runner.pid"
)

// Host is one fleet member: short name + runner API base. Token is the
// optional AdminToken for POST /api/reload (reload command); it may also
// come from --token or TIE_FLEET_ADMIN_TOKEN. Init selects the service
// manager for SSH restart/reload fallbacks: "systemd" (default) or "openrc".
// Name doubles as the SSH hostname; API is the HTTP base.
type Host struct {
	Name  string
	API   string
	Token string
	Init  string
}

// Rollout configures the `rollout` command: where the runner binary is
// built and what it is called on the fleet hosts. Deployment-specific by
// nature — the tool ships no defaults for BuildHost/RepoURL; rollout
// refuses to run until they are set.
type Rollout struct {
	BuildHost   string // SSH host with a toolchain and repo access
	RepoURL     string // git URL the build host clones/fetches
	RemoteBin   string // runner binary path on fleet hosts (default /usr/local/bin/tie-runner)
	ServiceName string // systemd/OpenRC service name (default tie-runner)
	PidFile     string // for OpenRC SIGHUP reload (default /run/tie-runner.pid)
}

// FleetConfig is the on-disk shape: optional top-level keys (seed,
// auth_user, remote_config) plus [[Hosts]] and [Rollout] tables.
type FleetConfig struct {
	Seed string
	// AuthUser is the Basic Auth username the runner expects (the password
	// is the real credential: AdminToken or DeployKey). Default "tie";
	// deployments that gate on username set it here.
	AuthUser string
	// RemoteConfig is the runner config file name on fleet hosts (home
	// dir), edited by specs/autostart and read by restart's pre-flight.
	RemoteConfig string
	Rollout      Rollout
	Hosts        []Host
}

func defaultFleet() FleetConfig {
	return FleetConfig{
		Seed: "https://runner1.example.com",
		Hosts: []Host{
			{Name: "runner1", API: "https://runner1.example.com"},
			{Name: "runner2", API: "https://runner2.example.com"},
			{Name: "runner3", API: "http://runner3.example.com:1166"},
		},
	}
}

// applyDefaults fills the tool-side defaults; the config file is the
// operator's, so defaults live in code, not in what gets written.
func (c *FleetConfig) applyDefaults() {
	if c.AuthUser == "" {
		c.AuthUser = defaultAuthUser
	}
	if c.RemoteConfig == "" {
		c.RemoteConfig = defaultRemoteConfig
	}
	if c.Rollout.RemoteBin == "" {
		c.Rollout.RemoteBin = defaultRemoteBin
	}
	if c.Rollout.ServiceName == "" {
		c.Rollout.ServiceName = defaultServiceName
	}
	if c.Rollout.PidFile == "" {
		c.Rollout.PidFile = defaultPidFile
	}
}

// loadFleetConfig resolves the full inventory (seed + hosts): explicit
// --conf path (ReadConfig), else conf.LoadConfig's search (current dir,
// user config dir, /etc).
func loadFleetConfig(explicit string) (FleetConfig, error) {
	var cfg FleetConfig
	path := explicit
	if explicit != "" {
		if err := conf.ReadConfig(explicit, &cfg); err != nil {
			return cfg, fmt.Errorf("fleet config %s: %w", explicit, err)
		}
	} else {
		var err error
		path, err = conf.LoadConfig(appName, configFile, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("no fleet config found (searched current dir, user config dir, /etc): %w\nrun 'tie-fleet init' to create one in the user config dir, or pass --conf PATH", err)
		}
	}
	cfg.Seed = strings.TrimRight(cfg.Seed, "/")
	for i := range cfg.Hosts {
		cfg.Hosts[i].API = strings.TrimRight(cfg.Hosts[i].API, "/")
		if cfg.Hosts[i].Name == "" || cfg.Hosts[i].API == "" {
			return cfg, fmt.Errorf("fleet config %s: [[Hosts]] entry needs both Name and API", path)
		}
	}
	if len(cfg.Hosts) == 0 {
		return cfg, fmt.Errorf("fleet config %s: no [[Hosts]] entries", path)
	}
	cfg.applyDefaults()
	return cfg, nil
}

// loadFleet resolves just the [[Hosts]] list.
func loadFleet(explicit string) ([]Host, error) {
	cfg, err := loadFleetConfig(explicit)
	if err != nil {
		return nil, err
	}
	return cfg.Hosts, nil
}

// runInit writes the default fleet config to the user config dir.
func runInit(args []string) error {
	if _, err := conf.LoadConfig(appName, configFile, &FleetConfig{}); err == nil {
		return fmt.Errorf("a tie-fleet.toml is already found; not overwriting")
	}
	path, err := conf.PathUserConfigDir(appName, configFile)
	if err != nil {
		return err
	}
	if err := conf.SaveToUserConfigDir(appName, configFile, defaultFleet()); err != nil {
		return err
	}
	fmt.Printf("wrote default fleet config to %s\n", path)
	return nil
}

func filterHosts(hosts []Host, only string) []Host {
	if only == "" {
		return hosts
	}
	var out []Host
	for _, h := range hosts {
		if h.Name == only {
			out = append(out, h)
		}
	}
	return out
}

// api wraps http.Client with the runner-endpoint helpers. Replies decode
// into the shared runner/api DTO types — the same single source of truth
// the runner itself marshals.
type api struct {
	c    *http.Client
	user string
}

// newAPI uses the system trust store by default; caPath, when given,
// replaces it with a single custom CA (for machines where the deployment's
// CA is not installed system-wide). TIE_FLEET_INSECURE=1 skips TLS
// verification — the bootstrap escape hatch for rotating a runner whose
// cert has already expired (its own /api/cert endpoint is unreachable
// otherwise).
func newAPI(caPath, authUser string) (*api, error) {
	tr := &http.Transport{}
	tlsCfg := &tls.Config{}
	if os.Getenv("TIE_FLEET_INSECURE") == "1" {
		tlsCfg.InsecureSkipVerify = true
	}
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA cert %s: no certificates found", caPath)
		}
		tlsCfg.RootCAs = pool
	}
	if tlsCfg.InsecureSkipVerify || tlsCfg.RootCAs != nil {
		tr.TLSClientConfig = tlsCfg
	}
	return &api{c: &http.Client{Transport: tr, Timeout: 15 * time.Second}, user: authUser}, nil
}

func (cl *api) capabilities(apiBase string) (runnerapi.CapabilityReply, error) {
	resp, err := cl.c.Get(apiBase + "/api/capabilities")
	if err != nil {
		return runnerapi.CapabilityReply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runnerapi.CapabilityReply{}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, apiBase)
	}
	var caps runnerapi.CapabilityReply
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		return runnerapi.CapabilityReply{}, fmt.Errorf("decode %s: %w", apiBase, err)
	}
	return caps, nil
}

// runningServices returns the names of services the runner reports as running.
func (cl *api) runningServices(apiBase string) ([]string, error) {
	var status map[string]runnerapi.TaskInfo
	resp, err := cl.c.Get(apiBase + "/api/services")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s/api/services", resp.StatusCode, apiBase)
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode %s: %w", apiBase, err)
	}
	var running []string
	for name, s := range status {
		if s.Status == runnerapi.TaskStatusRunning {
			running = append(running, name)
		}
	}
	return running, nil
}
