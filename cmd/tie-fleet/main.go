// tie-fleet administers the runner fleet: status, rollout, job-type
// specs, service autostart, restarts. Admin-only tool: rollout/restart need
// SSH + sudoers rights on the runner hosts.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = runInit(args)
	case "status":
		err = runStatus(args)
	case "rollout":
		err = runRollout(args)
	case "specs":
		err = runSpecs(args)
	case "autostart":
		err = runAutostart(args)
	case "restart":
		err = runRestart(args)
	case "reload":
		err = runReload(args)
	case "hosts":
		err = runHosts(args)
	case "cert":
		err = runCert(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `tie-fleet — runner fleet administration

usage: tie-fleet <command> [flags]

commands:
  init                         write the default fleet config to the user
                               config dir (~/.config/tie-fleet/tie-fleet.toml)
  status                       one line per runner: version, commit, load
  rollout [--rev R] [--only H] [--dry-run]
                               build once, then per host: idle-gate, swap
                               binary, restart, health-check, auto-rollback
  specs show                   capabilities per runner (API)
  specs edit [--host H] FILE|  textual, idempotent config edits with backup:
               --set name:cpu:rammb:gpu   add/update [JobTypeSpecs.name]
               --drop name                remove spec section + capability
               --add-cap name             append to Capabilities
               --dry-run                  print diff, write nothing
             with --host, FILE is ignored: the host's
             the runner config (remote_config key) is fetched, edited, and pushed back
  autostart list [--host H]    services with ServiceAutostart=false (adopting runners keep running services regardless)
  autostart set NAME... [--host H]
                               set ServiceAutostart=true for named services
  restart [--only H]           idle-gate + autostart warning + service restart
  hosts list                   fleet registry: pending/active hosts (seed API)
  hosts approve|remove <id>    approve a pending joiner / tombstone a host
                               (seed from the 'seed' key in tie-fleet.toml)
  cert status                  TLS cert expiry per runner (warns <30 days)
  cert push --only H --crt F --key F
                               remote cert install + hot swap (POST /api/cert,
                               no restart)
  reload [--only H] [--token T] [--restart]
                               hot config reload (no container restart): POST
                               /api/reload with the AdminToken (--token,
                               TIE_FLEET_ADMIN_TOKEN, or the [[Hosts]] token
                               key), else sudo systemctl reload; refuses
                               builds that predate hot reload (SIGHUP would kill them)

global flags (all commands): --conf PATH --ca CERT --ssh-user USER
fleet config: found via conf.LoadConfig search (current dir, then the user
config dir, then /etc/tie-fleet); --conf PATH overrides explicitly.
safety: rollout/restart refuse to touch the host the tool runs on unless
--allow-self (restarting the runner kills every service container on it,
including this tool's own session if it lives there).
`)
}

// commonFlags collects the flags every subcommand shares.
type commonFlags struct {
	conf    string
	ca      string
	sshUser string
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	c.sshUser = "tie"
	fs.StringVar(&c.conf, "conf", "", "explicit fleet config file (default: conf.LoadConfig search: current dir, user config dir, /etc/tie-fleet)")
	fs.StringVar(&c.ca, "ca", "", "CA certificate file for runner APIs (default: the system trust store)")
	fs.StringVar(&c.sshUser, "ssh-user", c.sshUser, "SSH user on runner hosts")
}
