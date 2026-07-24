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
	config, err := client.LoadConfig(configArg(os.Args))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error opening config file:", err)
	} else {
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
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "Config file to load", Value: "config.toml"},
		},
		Commands: []*cli.Command{
			cmdAdd(),
			cmdDel(),
			cmdGet(),
			cmdConf(),
			cmdImport(config),
			cmdMount(),
			cmdUpload(),
			cmdDownload(),
			cmdDump(),
			cmdRestore(),
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
