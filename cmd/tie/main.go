package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/version"

	"github.com/urfave/cli/v3"
)

var tie *client.TieClient

func main() {
	// Load config before building the command tree so `import` can generate a
	// subcommand per configured dir-type. The global flags aren't parsed yet at
	// this point, so peek them out of os.Args directly.
	configName, collection := parseGlobalFlags(os.Args)
	config, created, err := client.LoadOrCreateConfig(configName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error opening config file:", err)
	} else {
		if created {
			fmt.Fprintf(os.Stderr, "No config found; created a default at %s\n", config.Path())
		}
		tie = client.NewTieClientFor(config, collection)
	}

	cmd := &cli.Command{
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
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"C"}, Usage: "Config file to load (the .toml extension may be omitted)", Value: "config.toml"},
			&cli.StringFlag{Name: "collection", Aliases: []string{"c"}, Usage: "Collection to operate on (a name from [Collections]; empty uses DefaultCollection)"},
		},
		Commands: []*cli.Command{
			cmdAdd(),
			cmdDel(),
			cmdGet(),
			cmdConf(),
			cmdTag(),
			cmdVersions(),
			cmdImport(config),
			cmdMount(),
			cmdUpload(),
			cmdDownload(),
			cmdDump(),
			cmdRestore(),
			cmdVerify(),
			cmdStat(),
		},
	}
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "tie:", err)
		os.Exit(1)
	}
}

// parseGlobalFlags extracts the global -C/--config and -c/--collection values
// from the raw argument list before cli parses them (the command tree is built
// from config first). It scans only the leading run of global flags, which
// precede the subcommand, and stops at the first non-global token — so a local
// --collection on a subcommand (verify/import) is left untouched. configName
// matches the cli default when the flag is absent.
func parseGlobalFlags(args []string) (configName, collection string) {
	configName = "config.toml"
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
