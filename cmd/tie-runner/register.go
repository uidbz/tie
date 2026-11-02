package main

// Fleet join and the seed's host-config bootstrap. `tie-runner
// --register` posts the join handshake to the seed, waits for admin
// approval, and writes the seed-supplied config — the installer path for
// a new fleet host. buildHostConfig is the other side: the seed answers
// an approved host with the config document it should run, so a joined
// host needs nothing but the join token to start.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/fleet"

	"github.com/uidbz/conf"
)

const (
	fleetRegistryFile = "fleet-registry.json"
	registerPoll      = 5 * time.Second
	registerMaxWait   = 30 * time.Minute
)

// registerWithSeed runs the one-shot join. Exit codes: 0 registered and
// config written, 2 pending approval after the wait budget (the printed
// host ID is what the admin approves), 1 hard error.
func registerWithSeed(argv []string) int {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	seed := fs.String("seed", "", "seed runner URL (required)")
	name := fs.String("name", "", "host name (required)")
	url := fs.String("url", "", "this runner's public URL (required)")
	caps := fs.String("caps", "CPUv1", "comma-separated capabilities")
	token := fs.String("join-token", "", "fleet join token (empty = wait for admin approval)")
	fs.Parse(argv)
	if *seed == "" || *name == "" || *url == "" {
		fmt.Fprintln(os.Stderr, "register: --seed, --name and --url are required")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	req := api.RegisterRequest{
		Name:         *name,
		URL:          *url,
		Capabilities: splitCaps(*caps),
		JoinToken:    *token,
	}
	rep, err := fleet.Join(ctx, *seed, req, registerPoll, registerMaxWait, nil)
	var pending *fleet.PendingError
	switch {
	case errors.As(err, &pending):
		fmt.Printf("pending approval as host ID %s\n", pending.HostID)
		fmt.Printf("approve with: tie-fleet hosts approve %s\n", pending.HostID)
		return 2
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// The seed's reply embeds the config document (its schema belongs to
	// this binary); decode and write it as the normal config file.
	fresh := defaultServerConfig()
	if err := json.Unmarshal(rep.Config, &fresh); err != nil {
		fmt.Fprintf(os.Stderr, "decoding seed config reply: %v\n", err)
		return 1
	}
	if err := conf.SaveToCurrentDir(configFileName, &fresh); err != nil {
		fmt.Fprintf(os.Stderr, "writing %s: %v\n", configFileName, err)
		return 1
	}
	fmt.Printf("registered as %s; config written to %s\n", rep.HostID, configFileName)
	return 0
}

// buildHostConfig answers the register handshake with the config an
// approved host should run: the seed's shared settings (filehost, store,
// registry), the host's advertised capabilities, this fleet's other
// active hosts as backends, and the host's own admin token. Capacity is
// left at zero so the new host auto-detects its own machine; TLS starts
// empty — the runner joins plain HTTP and picks its cert via /api/cert.
func buildHostConfig(h *api.FleetHost) any {
	config.Lock()
	defer config.Unlock()
	c := &ServerConfig{
		Address:            ":443",
		Filehost:           config.Filehost,
		Store:              config.Store,
		DockerRegistry:     config.DockerRegistry,
		Capabilities:       slices.Clone(h.Capabilities),
		BuildArgs:          maps.Clone(config.BuildArgs),
		DebugLogging:       config.DebugLogging,
		JobDBPath:          config.JobDBPath,
		DefaultJobMemoryMB: config.DefaultJobMemoryMB,
		TotalCPU:           0, // auto-detect on the new host
		TotalRAMMB:         0,
		TotalGPU:           0,
		JobTypeSpecs:       maps.Clone(config.JobTypeSpecs),
		AuthUser:           config.AuthUser,
		AdminToken:         h.AdminToken,
		FleetJoinToken:     "", // the new host is not a seed
		FleetSeed:          config.FleetSeed,
	}
	for _, o := range fleetReg.List(true) {
		if o.ID != h.ID {
			c.Backends = append(c.Backends, o.URL)
		}
	}
	return c
}

func splitCaps(caps string) []string {
	var out []string
	for _, c := range strings.Split(caps, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}
