package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/version"

	"github.com/urfave/cli/v3"
)

func main() {
	// Load config before building the command tree so `import` can generate a
	// subcommand per configured dir-type. The global flags aren't parsed yet at
	// this point, so peek them out of os.Args directly.
	//
	// Loading never writes (§6.6): `tie --help`, a typo and every read-only
	// command used to leave a config file behind in the user config dir. A
	// missing config is reported by the Before hook, and `tie conf create` is
	// the only thing that writes one.
	configName, collection := parseGlobalFlags(os.Args)
	a := &app{}
	if config, err := client.LoadConfig(configName); err != nil {
		a.configErr = err
	} else {
		a.config = config
		a.tie = client.NewTieClientFor(config, collection)
	}

	root := newRootCommand(a)
	if err := root.Run(context.Background(), os.Args); err != nil {
		// One shape for every failure: `tie <command>: what went wrong`, in
		// lower case, with no second "Error:" inside the message (§6.8). The
		// command comes from the tree, so a nested command names itself.
		fmt.Fprintln(os.Stderr, errorPrefix(root, os.Args), err)
		os.Exit(exitCodeFor(err))
	}
}

// errorPrefix names the command that failed, so `tie tag files nope` says
// `tie tag files: …` instead of `tie: …` and the operator knows which step to
// re-run (§6.8).
func errorPrefix(root *cli.Command, args []string) string {
	if path := commandPath(root, args); path != "" {
		return "tie " + path + ":"
	}
	return "tie:"
}

// globalFlagWithValues are the flags that live before the command name and take
// a value, so the scan for the command path can step over them.
var globalFlagWithValues = map[string]bool{
	"-C": true, "--config": true, "-c": true, "--collection": true, "--format": true,
}

// commandPath walks the command tree along the argument list to name the command
// the operator actually ran ("tag files"). It stops at the first token that is
// not a subcommand, so arguments and flags are never mistaken for one.
func commandPath(root *cli.Command, args []string) string {
	var path []string
	cur := root
	for i := 1; i < len(args); i++ {
		a := args[i]
		if globalFlagWithValues[a] {
			i++
			continue
		}
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

// newRootCommand builds the whole command tree. It is a function rather than
// inline in main so a test can run the real tree — the same Before hook, the
// same global flags, the same exit codes (§6.5).
func newRootCommand(a *app) *cli.Command {
	return &cli.Command{
		Name:    "tie",
		Version: version.String(),
		Usage:   "Manage a tie triple-store and its content-addressed filehost",
		Description: "tie is the command-line client for the tie triple-store. It stores and\n" +
			"queries triples (key/value1/value2), uploads and downloads files to and\n" +
			"from a content-addressed filehost, imports and tags directory trees, and\n" +
			"mounts collections as a FUSE filesystem.",
		EnableShellCompletion: true,
		// Surface the auto-generated `completion` command (bash/zsh/fish/pwsh) in
		// help; cli registers it hidden by default. Its own description documents
		// the setup one-liner, e.g. `source <(tie completion bash)`.
		ConfigureShellCompletionCommand: func(c *cli.Command) { c.Hidden = false },
		// One guard for every command: a config that failed to load is reported
		// once, with the exit code that says so, instead of 31 copies of the same
		// check spread through the command tree.
		Before: a.before,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"C"}, Usage: "Config file to load (the .toml extension may be omitted). Precedence: -C, then $TIE_CONFIG, then ./config.toml, then the user and system config dirs.", Value: "config.toml"},
			&cli.StringFlag{Name: "collection", Aliases: []string{"c"}, Usage: "Collection to operate on (a name from [Collections]; empty uses DefaultCollection)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Report errors only; silence library warnings and progress"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}, Usage: "Also report what the library does per file"},
			&cli.StringFlag{Name: "format", Usage: "Output format for listings: tsv, json or table (default: table on a terminal, tsv otherwise)"},
		},
		Commands: []*cli.Command{
			cmdAdd(a),
			cmdDel(a),
			cmdGet(a),
			cmdConf(a),
			cmdVersion(a),
			cmdTag(a),
			cmdVersions(a),
			cmdImport(a),
			cmdMount(a),
			cmdUpload(a),
			cmdDownload(a),
			cmdDump(a),
			cmdRestore(a),
			cmdVerify(a),
			cmdRepair(a),
			cmdStat(a),
			cmdMedia(a),
			cmdHistory(a),
			cmdPlaylist(a),
		},
	}
}

// exitCodeFor maps a command's failure to the process status. An exitError
// carries its own (the fsck codes of §6.5: 1 problems, 2 usage, 4 could not
// run); anything else is the generic failure.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// parseGlobalFlags extracts the global -C/--config and -c/--collection values
// from the raw argument list before cli parses them (the command tree is built
// from config first). It scans only the leading run of global flags, which
// precede the subcommand, and stops at the first non-global token — so a local
// --collection on a subcommand (verify/import) is left untouched. configName
// matches the cli default when the flag is absent.
// parseGlobalFlags also honours $TIE_CONFIG, the conventional override for a
// tool whose config is otherwise found by searching (§6.6). -C still wins, so a
// wrapper script can set the env once and a one-off invocation can override it.
func parseGlobalFlags(args []string) (configName, collection string) {
	configName = "config.toml"
	if env := os.Getenv("TIE_CONFIG"); env != "" {
		configName = env
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "--config":
			if i+1 < len(args) {
				configName = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-C="):
			configName = strings.TrimPrefix(a, "-C=")
		case strings.HasPrefix(a, "--config="):
			configName = strings.TrimPrefix(a, "--config=")
		case a == "-c" || a == "--collection":
			if i+1 < len(args) {
				collection = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-c="):
			collection = strings.TrimPrefix(a, "-c=")
		case strings.HasPrefix(a, "--collection="):
			collection = strings.TrimPrefix(a, "--collection=")
		default:
			return // first non-global token = subcommand; stop scanning
		}
	}
	return
}
