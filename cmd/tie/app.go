package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
)

// app is what every command runs against: the loaded config and a client
// scoped to the collection the user selected. It replaces the package-level
// `tie` global (§7.6) — the global is what forced the command tree to be built
// after a pre-parse of os.Args, because a command that wanted the client had no
// way to be handed one.
type app struct {
	config client.Config
	tie    *client.TieClient
}

// configExempt are the commands that work without a loaded config: they report
// on the build itself, or create the config. The empty name is the bare `tie`
// invocation, which prints help.
func configExempt(name string) bool {
	switch name {
	case "", "version", "conf", "help", "completion", "generate-shell-completion":
		return true
	}
	return false
}

// before is the guard every command used to carry as its own
// `if tie == nil { return errors.New("Error: Config not loaded") }` — 31 copies
// of the same three lines. A config that failed to load is an environment
// failure rather than a failed operation, so it exits 2.
func (a *app) before(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	// The hook runs on the root command, so the command about to execute is the
	// first positional argument, not cmd.Name.
	if configExempt(cmd.Args().First()) {
		return ctx, nil
	}
	if a.tie == nil {
		return ctx, exitError{code: 2, msg: "config not loaded"}
	}
	// The library reports what it notices through a slog logger rather than
	// writing to stderr itself (§6.1); this is where it reaches the terminal.
	a.tie.Logger = cliLogger(cmd)
	return ctx, nil
}

// cliLogger is the stderr sink for library messages. No timestamps — a human
// scanning a terminal reads the message, not the clock. The default level shows
// warnings and what an import did to history, which is what the library used to
// print unconditionally; -q keeps errors, -v adds the per-file chatter.
func cliLogger(cmd *cli.Command) *slog.Logger {
	level := slog.LevelInfo
	switch {
	case cmd.Bool("quiet"):
		level = slog.LevelError
	case cmd.Bool("verbose"):
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
}

// exitError carries the exit code a failure should produce, so a command can
// distinguish "wrong environment" (2) from "the operation failed" (1).
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }
