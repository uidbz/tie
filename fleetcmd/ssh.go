// SSH/SCP plumbing. We shell out to the system ssh/scp rather than embedding
// an SSH library: the operator's keys, known_hosts, and sudoers setup keep
// working unchanged, and BatchMode=yes preserves the "fail fast, no prompts"
// behavior.
package fleetcmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type sshRunner struct {
	user string
}

func (s sshRunner) run(host, script string) (string, error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", s.user+"@"+host, script)
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if err != nil {
		return text, fmt.Errorf("ssh %s: %w: %s", host, err, text)
	}
	return text, nil
}

func (s sshRunner) copyTo(local, host, remote string) error {
	cmd := exec.Command("scp", "-q", "-o", "BatchMode=yes", local, s.user+"@"+host+":"+remote)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp to %s: %w: %s", host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s sshRunner) copyFrom(host, remote, local string) error {
	cmd := exec.Command("scp", "-q", "-o", "BatchMode=yes", s.user+"@"+host+":"+remote, local)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp from %s: %w: %s", host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// serviceCmd returns the sudo-prefixed service command for a host's init
// system: systemd (default) or openrc. OpenRC has no reload verb; SIGHUP to
// the pidfile process is the equivalent.
func serviceCmd(init, action, service, pidFile string) string {
	if init == "openrc" {
		if action == "reload" {
			return "sudo kill -HUP $(cat " + pidFile + ")"
		}
		return "sudo rc-service " + service + " " + action
	}
	return "sudo systemctl " + action + " " + service
}

// localHostname is the short hostname of this machine, used by the
// self-restart guard: restarting the runner that hosts this tool's own
// session drops the session mid-rollout.
func localHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.SplitN(h, ".", 2)[0]
}

// guardSelf refuses host unless allowSelf. Matches fleet names against the
// local short hostname and against the host part of the API base.
func guardSelf(h Host, allowSelf bool) error {
	if allowSelf {
		return nil
	}
	local := localHostname()
	if local == "" {
		return nil
	}
	if h.Name == local || strings.HasPrefix(local, h.Name) || strings.HasPrefix(h.Name, local) {
		return fmt.Errorf("%s looks like THIS machine: restarting its runner would kill this tool's own session (use --allow-self to override)", h.Name)
	}
	return nil
}
