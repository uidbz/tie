package executor

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/uidbz/tie/runner/api"
)

// The container marker protocol: the entrypoint binary reports control
// information on stdout as single lines of the form
//
//	#!TIE PercentDone 42.5
//	#!TIE SetResult <64-char content hash>
//	#!TIE SetError <message>
//
// Every other stdout line is ordinary log text. PercentDone carries a
// float whose scale is application-defined; SetResult may appear more than
// once (last wins); SetError accumulates.
//
// Protocol accepts a configurable set of marker tokens: deployments carry
// older container images that emit a legacy token, and the runner honors
// whichever tokens it is configured with, so old and new containers speak
// to the same runner.
const (
	ActionPercent = "PercentDone"
	ActionError   = "SetError"
	ActionResult  = "SetResult"
)

// DefaultMarker is the canonical marker token.
const DefaultMarker = "#!TIE"

// Protocol parses marker lines for a set of accepted tokens. The zero
// value accepts the canonical token only.
type Protocol struct {
	// Tokens are the leading marker strings accepted on stdout, e.g.
	// "#!TIE" plus any legacy tokens a deployment still runs. Order is
	// irrelevant; the longest matching token wins.
	Tokens []string
}

func (p Protocol) tokens() []string {
	if len(p.Tokens) == 0 {
		return []string{DefaultMarker}
	}
	return p.Tokens
}

// Marker reports the marker token a line carries, or "" if the line is
// ordinary log text.
func (p Protocol) Marker(line string) string {
	match := ""
	for _, t := range p.tokens() {
		if strings.HasPrefix(line, t) && len(t) > len(match) {
			match = t
		}
	}
	return match
}

// Command is one parsed marker: an Action and its Arg (the remainder of
// the line, re-joined with single spaces).
type Command struct {
	Action string
	Arg    string
}

// Parse splits a marker line into action and argument. marker reports
// whether the line carried any accepted token at all; valid reports
// whether it had an action word after the token. The argument is the raw
// remainder after the action word, spaces included — an error message's
// internal spacing survives.
func (p Protocol) Parse(line string) (cmd Command, marker, valid bool) {
	token := p.Marker(line)
	if token == "" {
		return Command{}, false, false
	}
	rest := strings.TrimLeft(strings.TrimPrefix(line, token), " ")
	if rest == "" {
		return Command{}, true, false
	}
	if sp := strings.Index(rest, " "); sp >= 0 {
		return Command{Action: rest[:sp], Arg: rest[sp+1:]}, true, true
	}
	return Command{Action: rest}, true, true
}

// Format renders one marker line for a container to print. tie-exec and
// container-side bindings build their output through this, so emitter and
// parser cannot drift.
func (p Protocol) Format(action, arg string) string {
	line := p.tokens()[0] + " " + action
	if arg != "" {
		line += " " + arg
	}
	return line
}

// Apply folds one stdout line into a job's live state: log always, marker
// side effects as defined above. It is what the local runtime calls per
// stdout line; deployments polling external backends reuse the same
// semantics for backend-emitted logs. Mutations go through the hooks'
// sinks when present (thread-safe), else straight into the record.
func (p Protocol) Apply(job *api.JobStatus, line string, hooks Hooks) {
	hooks.log(job, line)
	cmd, marker, valid := p.Parse(line)
	if !marker {
		return
	}
	if !valid {
		hooks.err(job, "WARNING: Incorrect marker command (missing argument):\n>>> "+line)
		slog.Warn("Invalid marker command", "handler", job.Handler, "line", line)
		return
	}
	switch cmd.Action {
	case ActionPercent:
		pct, err := strconv.ParseFloat(cmd.Arg, 32)
		if err == nil {
			hooks.percent(job, float32(pct))
			slog.Debug("Job progress update", "handler", job.Handler, "percent", pct)
		} else {
			hooks.log(job, "Error parsing PercentDone:"+err.Error())
			slog.Warn("Failed to parse PercentDone", "handler", job.Handler, "value", cmd.Arg, "error", err)
		}
	case ActionError:
		hooks.err(job, cmd.Arg)
		slog.Error("Job set error", "handler", job.Handler, "error_msg", cmd.Arg)
	case ActionResult:
		hooks.result(job, cmd.Arg)
		slog.Info("Job set result", "handler", job.Handler, "output_hash", cmd.Arg)
	}
}

// AddLine appends line to a newline-joined multi-line field.
func AddLine(orig, line string) string {
	if orig == "" {
		return line
	}
	return orig + "\n" + line
}
