package main

// Server resolution and the shared flags/formatting the listing commands
// all use: one --servers/--runner/--since/--user/--json constructor each,
// one table-style formatter set, one fleet discovery path. Duplicating
// these per command is how help texts and defaults drift apart.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"

	"github.com/urfave/cli/v3"
)

// parseServers splits a comma-separated --servers flag. When empty, it
// discovers the fleet from the seed runner via /api/fleet. Bare hostnames
// are given an https:// scheme so requests do not fail with "unsupported
// protocol scheme".
func parseServers(flag string) []string {
	if strings.TrimSpace(flag) == "" {
		return discoverFleet()
	}
	var out []string
	for _, s := range strings.Split(flag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, normalizeServer(s))
		}
	}
	if len(out) == 0 {
		return discoverFleet()
	}
	return out
}

// discoverFleet asks the seed runner for the full fleet. Without a
// configured seed (TIE_RUNNER) there is nothing to discover and the
// caller reports that; when the seed is unreachable the seed itself is
// the fleet, so single-runner setups keep working while the debug log
// notices discovery failed.
func discoverFleet() []string {
	if defaultRunner == "" {
		return nil
	}
	servers, err := runclient.GetFleet(defaultRunner)
	if err != nil || len(servers) == 0 {
		if debugMode {
			fmt.Fprintf(os.Stderr, "debug: fleet discovery from %s failed (%v); using the seed alone — it may be incomplete\n", defaultRunner, err)
		}
		return []string{defaultRunner}
	}
	return servers
}

// requireServers turns an empty fleet into the instruction that fixes it.
func requireServers(servers []string) error {
	if len(servers) == 0 {
		return errors.New("no runners to query: set " + runnerEnvVar + " or pass --servers host[,host]")
	}
	return nil
}

// normalizeServer ensures a runner URL has a scheme, defaulting to https://.
func normalizeServer(s string) string {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "https://" + s
	}
	return s
}

// Flag constructors. normalizeServer is applied in the action, not the
// flag, so the raw value stays visible in debug output.

func serversFlag() cli.Flag {
	return &cli.StringFlag{Name: "servers", Usage: "Comma-separated runner URLs (default: the fleet discovered from " + runnerEnvVar + ")"}
}

func runnerFlag() cli.Flag {
	return &cli.StringFlag{Name: "runner", Value: defaultRunner, Usage: "Runner URL (default: $" + runnerEnvVar + ")"}
}

func sinceFlag() cli.Flag {
	return &cli.StringFlag{Name: "since", Value: "30d", Usage: "Time window (e.g. 30d, 12h)"}
}

func userFlag() cli.Flag {
	return &cli.StringFlag{Name: "user", Usage: "Filter by user (use 'me' for current user)"}
}

// jsonFlag adds --json to the list commands: the same structs the
// tabwriter prints, rendered with encoding/json, so the CLI is scriptable
// without screen-scraping. The tabwriter stays the default.
func jsonFlag() cli.Flag {
	return &cli.BoolFlag{Name: "json", Usage: "Print JSON instead of a table"}
}

// resolvedRunner normalises --runner and turns the absent case into the
// instruction that fixes it.
func resolvedRunner(cmd *cli.Command) (string, error) {
	r := normalizeServer(strings.TrimSpace(cmd.String("runner")))
	if r == "" {
		return "", errors.New("no runner configured: set " + runnerEnvVar + " or pass --runner URL")
	}
	return r, nil
}

// resolveRunner picks the runner a status/logs query talks to: an
// explicit --runner wins (normalised); otherwise the job is looked up
// across the fleet — a job lives on the runner that accepted it, every
// other runner 404s. The found runner URL is returned so the caller can
// report it.
func resolveRunner(cmd *cli.Command, jobID string) (string, error) {
	if cmd.IsSet("runner") {
		return normalizeServer(cmd.String("runner")), nil
	}
	srv := parseServers(cmd.String("servers"))
	if err := requireServers(srv); err != nil {
		return "", err
	}
	runner, err := findJobRunner(srv, jobID)
	if err != nil {
		return "", err
	}
	return runner, nil
}

// findJobRunner asks each runner for the job's status; the first runner
// that answers (i.e. does not 404) owns the job. Errors other than 404
// are collected so an unreachable fleet is not mistaken for "the job
// does not exist" — the same model as runclient.FindPipeline.
func findJobRunner(servers []string, jobID string) (string, error) {
	var hard []error
	for _, s := range servers {
		var st api.JobStatus
		err := runclient.CallRunner("GET", s, "/api/status/"+url.PathEscape(jobID), nil, &st, 0)
		switch {
		case err == nil:
			return s, nil
		case errors.Is(err, runclient.ErrNotFound):
			// runner does not own this job; try the next one
		default:
			hard = append(hard, fmt.Errorf("%s: %w", s, err))
		}
	}
	if len(hard) > 0 {
		return "", fmt.Errorf("job %s not found on any reachable runner (%v)", jobID, hard)
	}
	return "", fmt.Errorf("job %s not found on any runner", jobID)
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// parseSinceDuration accepts "30d", "12h", "90m" etc. Empty means 30
// days; invalid input is an error, not a silent fallback to 30d.
func parseSinceDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 30 * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid --since %q: days must be a whole number (e.g. 30d)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --since %q: use a window like 30d, 12h or 90m", s)
	}
	return d, nil
}

func fmtUnix(ts int64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// fmtExitReason renders an empty (unknown/older) exit reason as "-".
func fmtExitReason(r string) string {
	if r == "" {
		return "-"
	}
	return r
}

// fmtMemMB renders peak memory in MB, or "-" when unknown/zero.
func fmtMemMB(mb int64) string {
	if mb <= 0 {
		return "-"
	}
	return strconv.FormatInt(mb, 10) + "MB"
}

// fmtCPU renders a core count, or "-" when unknown/zero.
func fmtCPU(cores int) string {
	if cores <= 0 {
		return "-"
	}
	return strconv.Itoa(cores)
}

// fmtPort renders a service's external host port, or "-" when unassigned.
func fmtPort(p int) string {
	if p <= 0 {
		return "-"
	}
	return strconv.Itoa(p)
}

// shortHash trims a tree hash to its first 12 chars for table display.
func shortHash(h string) string {
	if h == "" {
		return "-"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// shortServer trims the scheme for compact table display.
func shortServer(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return s
}

// fmtVersion renders a runner's build info as "vX.Y.Z (abc1234)", or "-"
// when the runner predates version reporting.
func fmtVersion(c *api.CapabilityReply) string {
	if c.Version == "" {
		return "-"
	}
	if c.GitCommit == "" {
		return c.Version
	}
	return c.Version + " (" + c.GitCommit + ")"
}

// joinCaps renders the capability list sorted and comma-joined. It copies
// before sorting: sorting the caller's slice in place would mutate the
// CapabilityReply the caller may still use.
func joinCaps(caps []string) string {
	if len(caps) == 0 {
		return "-"
	}
	sorted := slices.Clone(caps)
	slices.Sort(sorted)
	return strings.Join(sorted, ",")
}

// reportErrors prints any per-server fetch errors to stderr without
// aborting.
func reportErrors(errs map[string]error) {
	for server, err := range errs {
		fmt.Fprintf(os.Stderr, "Warning: %s unreachable: %s\n", server, err.Error())
	}
}

// pickFrom presents a numbered chooser over labels on a real terminal and
// returns the chosen index. It refuses on a pipe or CI run rather than
// blocking: the caller's error names the explicit form of the command.
func pickFrom(title string, labels []string) (int, error) {
	if !termIsTerminal() {
		return 0, fmt.Errorf("%s needs an interactive choice, but stdin is not a terminal: pass the ID explicitly", title)
	}
	fmt.Println(title)
	for i, l := range labels {
		fmt.Printf("  %d) %s\n", i+1, l)
	}
	fmt.Print("Choice: ")
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return 0, err
		}
		return 0, errors.New("no choice made")
	}
	n, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
	if err != nil || n < 1 || n > len(labels) {
		return 0, fmt.Errorf("choice must be a number from 1 to %d", len(labels))
	}
	return n - 1, nil
}

// insecureWarnOnce makes the http:// warning fire once per process, not
// once per authenticated call.
var insecureWarnOnce sync.Once

// checkAuthScheme warns before a DeployKey is about to be sent with Basic
// Auth to a plaintext http:// runner. Bare hosts normalize to https:// and
// stay silent. Called by every command that authenticates.
func checkAuthScheme(runner string) {
	if !strings.HasPrefix(normalizeServer(strings.TrimSpace(runner)), "http://") {
		return
	}
	insecureWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "WARNING: sending DeployKey to insecure http:// runner %s\n", runner)
	})
}
