package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"
)

// argKeys returns the positional arguments with every "-" replaced by the lines
// read from stdin, one key per line (§6.7). It is what makes
//
//	tie tag files jazz | cut -f1 | tie tag set - mellow
//
// work without xargs and its quoting traps. A command that takes a fixed
// argument list (download) checks for "-" itself.
func argKeys(ctx *cli.Command) ([]string, error) {
	args := ctx.Args().Slice()
	if !wantsStdin(ctx) {
		return args, nil
	}
	keys, err := readStdinKeys()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(args)+len(keys))
	for _, a := range args {
		if a == "-" {
			out = append(out, keys...)
			continue
		}
		out = append(out, a)
	}
	if !hasStdinArg(args) {
		// --stdin with no "-" placeholder: the keys are simply appended.
		out = append(out, keys...)
	}
	return out, nil
}

// hasStdinArg reports whether the arguments ask for keys on stdin.
func hasStdinArg(args []string) bool {
	for _, a := range args {
		if a == "-" {
			return true
		}
	}
	return false
}

// wantsStdin reports whether this invocation asked for keys on stdin — by the
// --stdin flag or by a lone "-" argument.
//
// The flag exists because of how the argument parser treats "-": it keeps it and
// stops parsing, so anything written *after* it never reaches the command.
// `tie get - --not cool` would lose the exclusion, and `tie tag set - blue`
// would lose the tags and clear them. --stdin is the spelling that survives
// arguments on either side (§6.7).
func wantsStdin(ctx *cli.Command) bool {
	return ctx.Bool("stdin") || hasStdinArg(ctx.Args().Slice())
}

// stdinFlag is the flag every command that reads keys from stdin carries.
func stdinFlag() cli.Flag {
	return &cli.BoolFlag{
		Name:  "stdin",
		Usage: "Read keys from stdin, one per line (the same as a lone -, which also works only as the last argument)",
	}
}

// warnDroppedArgs says out loud what the argument parser did: a lone "-" keeps
// itself and throws away everything written after it, so `tie get - --not cool`
// quietly loses the exclusion. The operator asked for stdin keys either way, so
// the run is not wrong — but the filter they typed is gone, and silence about
// that is the kind of surprise §6.6 is about.
func warnDroppedArgs() {
	for i, a := range os.Args {
		if a == "-" && i+1 < len(os.Args) {
			fmt.Fprintln(os.Stderr, "note: arguments after - are not parsed; write --stdin to keep them")
			return
		}
	}
}

// readStdinKeys reads one key per line, skipping blanks.
func readStdinKeys() ([]string, error) {
	var keys []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			keys = append(keys, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, operationalError("reading keys from stdin: %v", err)
	}
	return keys, nil
}
