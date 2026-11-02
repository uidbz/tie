package main

// The runner configuration document (tie-runner-config.toml, read from the
// working directory at startup). The key spellings match the historical
// runner config: an existing file loads as-is. Sections the open-source
// core does not implement (external batch backends, default-website
// proxying) are ignored on load; a deployment that needs them brings them
// as a shell around this binary.
//
// The file is hot-reloaded (SIGHUP or POST /api/reload) for the
// runtime-mutable keys; see reload.go. The runner itself rewrites the
// DeployedImages section on every deploy, so that section is a
// runner-managed artifact, not something reload overwrites.

import (
	"maps"
	"slices"
	"sync"

	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/server"

	"github.com/uidbz/conf"
)

const configFileName = "tie-runner-config.toml"

type ServerConfig struct {
	Address  string
	TLSCert  string
	TLSKey   string
	Filehost string
	// Store selects the named filehost store job containers upload their
	// results to (the Tie-Store header). Empty targets the filehost's
	// default store.
	Store          string
	DockerRegistry string
	Capabilities   []string
	Backends       []string
	BuildArgs      map[string]string
	DeployedImages []images.Image
	DebugLogging   bool
	// JobDBPath is the directory holding the embedded job-history store.
	// The key name is kept for config compatibility with the historical
	// runner even though the SQLite file it named is gone.
	JobDBPath string
	// DefaultJobMemoryMB caps every job container's memory when the
	// job-type spec carries none; 0 means no cap.
	DefaultJobMemoryMB int
	// TotalCPU/TotalRAMMB/TotalGPU are the resource pool the scheduler
	// admits jobs against. 0 auto-detects from the host at startup.
	TotalCPU   int
	TotalRAMMB int
	TotalGPU   int
	// JobTypeSpecs maps a local job type to its per-container resource
	// sizing, merged over the engine's built-in defaults.
	JobTypeSpecs map[string]executor.Spec
	// AuthUser is the Basic Auth user every management request must
	// carry. Empty (the default) uses the tie default.
	AuthUser string
	// AdminToken gates POST /api/reload, the fleet registry endpoints,
	// and image replication. Empty disables them over HTTP; the config
	// file remains the way in.
	AdminToken string
	// FleetJoinToken marks this runner as the fleet registry SEED: it
	// accepts POST /api/fleet/register from hosts presenting this token.
	// Empty = not a seed.
	FleetJoinToken string
	// FleetSeed is the seed runner's URL on non-seed hosts; registry
	// operations are proxied to it.
	FleetSeed string
	// ExtDir is the host-side scratch base bind-mounted into containers
	// at /ext (empty uses the engine default).
	ExtDir string
	// FileServer publishes a directory (client binaries, CA certs) from
	// the runner itself. Startup-only.
	FileServer server.FileServerConfig

	mu sync.Mutex
}

// Lock/Unlock expose the config mutex the same way the embedded mutex did,
// keeping every reader's discipline identical.
func (c *ServerConfig) Lock()   { c.mu.Lock() }
func (c *ServerConfig) Unlock() { c.mu.Unlock() }

// defaultServerConfig returns the built-in defaults. main() seeds the
// global with it and layers the TOML file over it; reloadConfig parses
// into a fresh copy so keys absent from the file fall back to the
// defaults exactly like at startup.
func defaultServerConfig() ServerConfig {
	return ServerConfig{
		Address:      ":1163",
		Filehost:     "https://localhost:1162",
		Capabilities: []string{"CPUv1"},
	}
}

var config = defaultServerConfig()

// Save writes the config back to the working directory. Deploys use it to
// persist the deployed-images table.
func (c *ServerConfig) Save() error {
	return conf.SaveToCurrentDir(configFileName, c)
}

// --- accessors ------------------------------------------------------------
// Every value the HTTP surface reads goes through one of these so hot
// reload takes effect on the next request. Slices and maps are cloned:
// callers may sort or iterate them while a reload swaps the config.

func cfgStr(get func(c *ServerConfig) string) func() string {
	return func() string {
		config.Lock()
		defer config.Unlock()
		return get(&config)
	}
}

var (
	filehostAccess     = cfgStr(func(c *ServerConfig) string { return c.Filehost })
	storeNameAccess    = cfgStr(func(c *ServerConfig) string { return c.Store })
	dockerRegistry     = cfgStr(func(c *ServerConfig) string { return c.DockerRegistry })
	adminTokenAccess   = cfgStr(func(c *ServerConfig) string { return c.AdminToken })
	fleetJoinToken     = cfgStr(func(c *ServerConfig) string { return c.FleetJoinToken })
	fleetSeedAccess    = cfgStr(func(c *ServerConfig) string { return c.FleetSeed })
	extDirAccess       = cfgStr(func(c *ServerConfig) string { return c.ExtDir })
	authUserAccess     = cfgStr(func(c *ServerConfig) string { return c.AuthUser })
	tlsCertPathAccess  = cfgStr(func(c *ServerConfig) string { return c.TLSCert })
	tlsKeyPathAccess   = cfgStr(func(c *ServerConfig) string { return c.TLSKey })
	capabilitiesAccess = func() []string {
		config.Lock()
		defer config.Unlock()
		return slices.Clone(config.Capabilities)
	}
	backendsAccess = func() []string {
		config.Lock()
		defer config.Unlock()
		return slices.Clone(config.Backends)
	}
	buildArgsAccess = func() map[string]string {
		config.Lock()
		defer config.Unlock()
		return maps.Clone(config.BuildArgs)
	}
)

// cfgJobTypeSpec resolves a job type against the live JobTypeSpecs map
// over the engine's built-in defaults.
func cfgJobTypeSpec(jobType string) (executor.Spec, bool) {
	config.Lock()
	specs := config.JobTypeSpecs
	config.Unlock()
	return executor.SpecFor(jobType, specs)
}

// cfgPool returns the configured pool totals (0 = auto-detect per axis).
func cfgPool() (cpu, ramMB, gpu int) {
	config.Lock()
	defer config.Unlock()
	return config.TotalCPU, config.TotalRAMMB, config.TotalGPU
}
