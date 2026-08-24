package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/version"

	"github.com/urfave/cli/v3"
)

var tie *client.TieClient

func main() {
	// Load config before building the command tree so `import` can generate a
	// subcommand per configured dir-type. The -c/--config flag isn't parsed yet
	// at this point, so peek it out of os.Args directly.
	configName := configArg(os.Args)
	config, created, err := client.LoadOrCreateConfig(configName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error opening config file:", err)
	} else {
		if created {
			fmt.Fprintf(os.Stderr, "No config found; created a default at %s\n", config.Path())
		}
		tie = client.NewTieClient(config)
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
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "Config file to load (the .toml extension may be omitted)", Value: "config.toml"},
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

// configArg extracts the value of the global -c/--config flag from the raw
// argument list, matching the cli default when the flag is absent. It only
// needs to find the global flag (which precedes any subcommand), so a simple
// left-to-right scan is enough.
func configArg(args []string) string {
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "-c="):
			return strings.TrimPrefix(a, "-c=")
		case strings.HasPrefix(a, "--config="):
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return "config.toml"
}
