// tie-run is the command-line client for a tie runner: submit jobs,
// deploy projects, and inspect the fleet. It speaks the frozen runner
// protocol (docs/runner-api.md) through the runclient package; every
// request and response on the wire is the runner/api DTO the runner
// itself marshals.
//
// The seed runner comes from TIE_RUNNER (or --runner/--servers). The
// per-image DeployKey bookkeeping lives in tie-project.toml next to the
// project and in <UserConfigDir>/tie/tie-run-config.toml (see config.go),
// never in the runner's replies' wake — the runner hands the key back
// exactly once, at the deploy that mints it.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/uidbz/tie/version"

	"github.com/urfave/cli/v3"
)

const (
	projectFile = "tie-project.toml"
	// runnerEnvVar names the seed runner the client talks to when no
	// --runner/--servers flag is given.
	runnerEnvVar = "TIE_RUNNER"
)

// debugMode mirrors the --debug flag for the commands. runclient dumps
// every outgoing management request when TIE_DEBUG is set; the Before
// hook turns on both views from one flag.
var debugMode bool

// defaultRunner resolves the seed runner at init: TIE_RUNNER wins and
// bare hosts get the https:// scheme. When it is unset, commands that
// need a runner say so instead of guessing one.
var defaultRunner = resolveDefaultRunner()

func resolveDefaultRunner() string {
	if v := strings.TrimSpace(os.Getenv(runnerEnvVar)); v != "" {
		return normalizeServer(v)
	}
	return ""
}

func main() {
	root := newRootCommand()

	// signal.NotifyContext: Ctrl-C or SIGTERM cancels the app context,
	// which the watching commands (status, logs, run, pipeline show
	// --follow) use to stop polling cleanly and print how to resume,
	// instead of the process dying mid-line.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := root.Run(ctx, os.Args)
	if err != nil {
		// One shape for every failure: `tie-run <command>: what went
		// wrong`, with errors on stderr so list output stays pipeable.
		fmt.Fprintln(os.Stderr, errorPrefix(root, os.Args), err)
		os.Exit(exitCodeFor(err))
	}
}

// newRootCommand builds the whole command tree.
func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:    "tie-run",
		Version: version.String(),
		Usage:   "Run and deploy projects on tie runners",
		Description: "tie-run submits jobs to a tie runner, deploys projects, and inspects\n" +
			"the fleet: jobs, queues, capacity, services and pipelines. Point it at a\n" +
			"runner with TIE_RUNNER or --runner/--servers.",
		EnableShellCompletion: true,
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if cmd.Bool("debug") {
				debugMode = true
				// One switch for both views: runclient reads TIE_DEBUG.
				os.Setenv("TIE_DEBUG", "1")
			}
			return ctx, nil
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "debug", Usage: "Verbose messaging, including every management request (also TIE_DEBUG=1)"},
		},
		Commands: []*cli.Command{
			cmdDeploy(),
			cmdRun(),
			cmdStatus(),
			cmdLogs(),
			cmdJobs(),
			cmdQueue(),
			cmdActive(),
			cmdCapacity(),
			cmdServices(),
			cmdCancel(),
			cmdPipeline(),
			cmdDelete(),
			cmdVersion(),
		},
	}
}

// errorPrefix names the command that failed, so `tie-run services stop
// x` says `tie-run services stop: …` and the operator knows which step
// to re-run.
func errorPrefix(root *cli.Command, args []string) string {
	if path := commandPath(root, args); path != "" {
		return "tie-run " + path + ":"
	}
	return "tie-run:"
}

// commandPath walks the command tree along the argument list to name the
// command the operator actually ran ("services stop"). It stops at the
// first token that is not a subcommand, so arguments and flags are
// never mistaken for one.
func commandPath(root *cli.Command, args []string) string {
	var path []string
	cur := root
	for i := 1; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			break
		}
		next := cur.Command(a)
		if next == nil {
			break
		}
		path = append(path, a)
		cur = next
	}
	return strings.Join(path, " ")
}

// exitCodeFor maps a command's failure to the process status. A cli
// ExitCoder carries its own code; anything else is the generic failure.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}
