package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	flags := parseGlobalFlags(os.Args)
	a := &app{}

	config, loadErr := client.LoadConfig(flags.configName)
	if loadErr == nil {
		a.configLoaded = true
	}
	switch {
	case flags.dbFile != "":
		config, loadErr = configureDBFile(config, loadErr, flags)
	case flags.readOnly || flags.reverseRelations != "":
		loadErr = errors.New("--read-only and --reverse-relations require -f/--file")
	}
	if loadErr != nil {
		a.configErr = loadErr
	} else {
		a.config = config
		a.tie = client.NewTieClientFor(config, flags.collection)
	}

	root := newRootCommand(a)
	err := root.Run(context.Background(), os.Args)
	// Flush and release a locally opened database before exiting. A server
	// drains on SIGINT/SIGTERM; the CLI has exactly one command to finish, so a
	// plain Close after Run covers the normal (and error) paths.
	if a.tie != nil {
		_ = a.tie.Close()
	}
	if err != nil {
		// One shape for every failure: `tie <command>: what went wrong`, in
		// lower case, with no second "Error:" inside the message (§6.8). The
		// command comes from the tree, so a nested command names itself.
		fmt.Fprintln(os.Stderr, errorPrefix(root, os.Args), err)
		os.Exit(exitCodeFor(err))
	}
}

// dbFileError marks a failure to open the -f/--file database (as opposed to a
// config failure), so the Before hook names the right file.
type dbFileError struct{ err error }

func (e dbFileError) Error() string { return e.err.Error() }
func (e dbFileError) Unwrap() error { return e.err }

// configureDBFile applies a -f/--file direct-database choice to the loaded
// config. A missing config is fine in file mode (the database needs none); an
// explicitly requested config that is broken is not. Filehosts, ImportDest and
// PrevVersions survive, so an offline triplestore still uploads/imports through
// the configured filehost.
func configureDBFile(config client.Config, loadErr error, flags globalFlags) (client.Config, error) {
	fileCfg, err := client.ConfigForFile(flags.dbFile)
	if err != nil {
		return config, dbFileError{fmt.Errorf("cannot open %s: %w", flags.dbFile, err)}
	}
	rels, err := parseRelations(flags.reverseRelations)
	if err != nil {
		return config, err
	}
	fileCfg.ReadOnly = flags.readOnly
	if loadErr == nil {
		// The config's direct-mode choices survive an -f override unless the
		// flags say otherwise.
		fileCfg.ReadOnly = fileCfg.ReadOnly || config.ReadOnly
		if len(rels) == 0 {
			rels = config.ReverseRelations
		}
	}
	fileCfg.ReverseRelations = rels

	if loadErr != nil {
		if flags.configExplicit || !os.IsNotExist(loadErr) {
			return config, loadErr
		}
		return fileCfg, nil
	}

	config.TripleStoreFile = fileCfg.TripleStoreFile
	config.TripleStoreURL = ""
	config.ReadOnly = fileCfg.ReadOnly
	config.ReverseRelations = fileCfg.ReverseRelations
	if flags.collection == "" {
		// -f names the database to open; the config's DefaultCollection must not
		// redirect the invocation to a different (possibly absent) sibling.
		config.Collection = fileCfg.Collection
		config.DefaultCollection = fileCfg.Collection
	}
	return config, nil
}

// parseRelations turns the comma-separated direct-mode reverse-index list into
// names. Empty means "index every relation" (the correct default for an unknown
// server configuration); an explicitly empty list is a usage error.
func parseRelations(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--reverse-relations needs at least one relation name")
	}
	return out, nil
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
	"-f": true, "--file": true, "--reverse-relations": true,
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
			&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Open a local .tie database file directly, without a triplestore server (filehost operations still use the config)"},
			&cli.BoolFlag{Name: "read-only", Usage: "With --file: open the database read-only (no lock, safe beside a running mount; write requests are refused)"},
			&cli.StringFlag{Name: "reverse-relations", Usage: "With --file: comma-separated relations to index in reverse (default: every relation; set to the server's subset for a large database)"},
			&cli.StringFlag{Name: "collection", Aliases: []string{"c"}, Usage: "Collection to operate on (a name from [Collections]; empty uses DefaultCollection; with --file, a sibling <name>.tie in the same directory)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Report errors only; silence library warnings and progress"},
			&cli.BoolFlag{Name: "verbose", Aliases: []string{"V"}, Usage: "Also report what the library does per file"},
			&cli.StringFlag{Name: "format", Usage: "Output format for listings: tsv, json or table (default: table on a terminal, tsv otherwise)"},
		},
		Commands: []*cli.Command{
			cmdAdd(a),
			cmdDel(a),
			cmdGet(a),
			cmdSearch(a),
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

// globalFlags is the leading run of flags that must be known before the
// command tree is built: they select the config and the store.
type globalFlags struct {
	configName       string
	collection       string
	dbFile           string
	reverseRelations string
	readOnly         bool
	configExplicit   bool
}

// parseGlobalFlags extracts the global -C/--config, -c/--collection,
// -f/--file, --read-only and --reverse-relations values from the raw argument
// list before cli parses them (the command tree is built from config first). It
// scans only the leading run of global flags, which precede the subcommand, and
// stops at the first non-global token — so a local --collection or -f on a
// subcommand (verify/import/get's filter) is left untouched. configName matches
// the cli default when the flag is absent; configExplicit reports whether -C or
// $TIE_CONFIG named a file, so -f can tolerate a missing default config but
// still report a broken explicit one.
//
// $TIE_CONFIG is the conventional override for a tool whose config is otherwise
// found by searching (§6.6). -C still wins, so a wrapper script can set the env
// once and a one-off invocation can override it.
func parseGlobalFlags(args []string) globalFlags {
	flags := globalFlags{configName: "config.toml"}
	if env := os.Getenv("TIE_CONFIG"); env != "" {
		flags.configName = env
		flags.configExplicit = true
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "--config":
			if i+1 < len(args) {
				flags.configName = args[i+1]
				flags.configExplicit = true
				i++
			}
		case strings.HasPrefix(a, "-C="):
			flags.configName = strings.TrimPrefix(a, "-C=")
			flags.configExplicit = true
		case strings.HasPrefix(a, "--config="):
			flags.configName = strings.TrimPrefix(a, "--config=")
			flags.configExplicit = true
		case a == "-f" || a == "--file":
			if i+1 < len(args) {
				flags.dbFile = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-f="):
			flags.dbFile = strings.TrimPrefix(a, "-f=")
		case strings.HasPrefix(a, "--file="):
			flags.dbFile = strings.TrimPrefix(a, "--file=")
		case a == "--read-only":
			flags.readOnly = true
		case strings.HasPrefix(a, "--read-only="):
			if v, err := strconv.ParseBool(strings.TrimPrefix(a, "--read-only=")); err == nil {
				flags.readOnly = v
			}
		case a == "--reverse-relations":
			if i+1 < len(args) {
				flags.reverseRelations = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--reverse-relations="):
			flags.reverseRelations = strings.TrimPrefix(a, "--reverse-relations=")
		case a == "-c" || a == "--collection":
			if i+1 < len(args) {
				flags.collection = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-c="):
			flags.collection = strings.TrimPrefix(a, "-c=")
		case strings.HasPrefix(a, "--collection="):
			flags.collection = strings.TrimPrefix(a, "--collection=")
		default:
			return flags // first non-global token = subcommand; stop scanning
		}
	}
	return flags
}
