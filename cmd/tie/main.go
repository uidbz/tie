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

// globalFlagSpecs is the one table of global flag spellings and value arity
// (#155): the leading scan (parseGlobalFlags), the command-path walk
// (commandPath) and the after-subcommand guard (lateGlobalFlagErr) all read
// it, so a new global flag is defined once.
type globalFlagSpec struct{ value bool }

var globalFlagSpecs = map[string]globalFlagSpec{
	"-C": {value: true}, "--config": {value: true},
	"-c": {value: true}, "--collection": {value: true},
	"-f": {value: true}, "--file": {value: true},
	"--reverse-relations": {value: true},
	"--read-only": {},
	"-q": {}, "--quiet": {},
	"-V": {}, "--verbose": {},
	"--format": {value: true},
	"-h": {}, "--help": {},
	"-v": {}, "--version": {},
}

// lateGlobalCanonical folds each construction-global spelling (no dashes)
// to its canonical flag name. These are the globals that decide how the
// client is constructed; a spelling AFTER the subcommand — which cli v3
// happily parses at root level — is silently dropped unless the command
// reads it (inheritedReaders) (#143).
var lateGlobalCanonical = map[string]string{
	"config": "config", "C": "config",
	"collection": "collection", "c": "collection",
	"file": "file", "f": "file",
	"read-only":       "read-only",
	"reverse-relations": "reverse-relations",
}

// inheritedReaders names the subcommands that READ a construction global
// from the root — cli v3 lets a subcommand's context fall back to parent
// flags — so spelling it after that subcommand really does apply it, and the
// guard must stay out (#143): `conf show -c X` prints the collection it
// resolves, `conf create -C x` writes where -C says, and the offline dump
// honours --read-only. Everywhere else a trailing construction global is
// parsed and dropped, which is what the guard rejects.
var inheritedReaders = map[string]map[string]bool{
	"conf show":   {"collection": true},
	"conf create": {"config": true},
	"dump":        {"read-only": true},
}

// flagSpelling splits an argument into the flag it spells and any =-attached
// value: "--file=x" -> ("--file", "x", true); "-f" -> ("-f", "", false).
func flagSpelling(arg string) (name, value string, attached bool) {
	name, value, attached = strings.Cut(arg, "=")
	return name, value, attached
}

// commandPath walks the command tree along the argument list to name the command
// the operator actually ran ("tag files"). It stops at the first token that is
// not a subcommand, so arguments and flags are never mistaken for one.
func commandPath(root *cli.Command, args []string) string {
	_, path, _ := descendCommandPath(root, args, 1)
	return strings.Join(path, " ")
}

// descendCommandPath walks root's subcommand tree along args starting at
// start, stepping over global flags with their values. It returns the deepest
// command reached, the path walked, and the index where that command's own
// arguments begin — commandPath's prefix and lateGlobalFlagErr's scan point
// come from the same walk (#155).
func descendCommandPath(root *cli.Command, args []string, start int) (*cli.Command, []string, int) {
	cur := root
	path := []string{}
	i := start
	seenCommand := false
	for i < len(args) {
		a := args[i]
		// Global spellings step over only BEFORE the subcommand: after it,
		// every flag belongs to the command (cli keeps the raw tokens in the
		// root args), so skipping one as a global would hide it from the
		// late-flag scan.
		if spec, known := globalFlagSpecs[a]; known && !seenCommand {
			if spec.value {
				i++
			}
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
		seenCommand = true
		cur = next
		i++
	}
	return cur, path, i
}

// lateGlobalFlagErr rejects construction globals spelled after the subcommand
// (#143). cli v3 parses root-level flags anywhere in the list and then hands
// the command its action without them, so `tie -f db add k tag v --read-only`
// used to exit 0 with the database written — a safety flag silently ignored.
// The check runs in the Before hook, where cli still exposes the raw tokens in
// the root command's args. A spelling the subcommand defines as its own flag
// (get's -f filter, dump's --file, import/verify/repair's --collection) is the
// subcommand's business and stays legal.
func lateGlobalFlagErr(root *cli.Command, args []string) error {
	cur, path, i := descendCommandPath(root, args, 0)
	pathStr := strings.Join(path, " ")
	// Arity of the leaf's own flags, so the scan steps over a local flag's
	// value instead of mistaking it for a token.
	local := map[string]bool{}
	for _, f := range cur.Flags {
		takesValue := true
		switch f.(type) {
		case *cli.BoolFlag, *cli.BoolWithInverseFlag:
			takesValue = false
		}
		for _, n := range f.Names() {
			local[n] = takesValue
		}
	}
	for ; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			return nil
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			continue
		}
		name, _, attached := flagSpelling(tok)
		trimmed := strings.TrimLeft(name, "-")
		if takes, isLocal := local[trimmed]; isLocal {
			if takes && !attached {
				i++
			}
			continue
		}
		canonical, isLate := lateGlobalCanonical[trimmed]
		if !isLate {
			continue
		}
		if inheritedReaders[pathStr][canonical] {
			// This command reads the inherited flag: it applies, so the
			// placement is legal even though it is not a local flag.
			if spec, known := globalFlagSpecs[name]; known && spec.value && !attached {
				i++
			}
			continue
		}
		return exitError{code: exitUsage,
			msg: fmt.Sprintf("%s is a global flag: put it before the subcommand", name)}
	}
	return nil
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
// list before cli parses them (the command tree is built from config first).
// It scans only the leading run of global flags, which precede the subcommand
// — but EVERY global spelling, including the boolean -q/-V/-h/-v and the
// value-taking --format, keeps the scan going: stopping at those dropped a
// later -f and silently opened the configured server instead of the -f file
// (#144). The table (globalFlagSpecs) is shared with the command walk and the
// after-subcommand guard (#155). configName matches the cli default when the
// flag is absent; configExplicit reports whether -C or $TIE_CONFIG named a
// file, so -f can tolerate a missing default config but still report a broken
// explicit one.
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
		name, value, attached := flagSpelling(args[i])
		spec, known := globalFlagSpecs[name]
		if !known {
			return flags // first non-global token = subcommand; stop scanning
		}
		if spec.value {
			if !attached {
				if i+1 < len(args) {
					value = args[i+1]
				}
				i++
			}
		} else if !attached && name == "--read-only" {
			value = "true"
		}
		switch name {
		case "-C", "--config":
			flags.configName = value
			flags.configExplicit = true
		case "-f", "--file":
			flags.dbFile = value
		case "-c", "--collection":
			flags.collection = value
		case "--reverse-relations":
			flags.reverseRelations = value
		case "--read-only":
			if v, err := strconv.ParseBool(value); err == nil {
				flags.readOnly = v
			}
		}
		// -q/-V/--format/-h/-v: consumed so the scan keeps going; cli applies
		// them itself.
	}
	return flags
}
