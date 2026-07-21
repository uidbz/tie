package main

import (
	"context"
	"log"
	"os"
	"strings"

	"git.sr.ht/~uid/tie/client"

	"github.com/urfave/cli/v3"
)

var (
	stdin         []Stdin
	outputAsTable bool
	configFile    string
	tie           *client.TieClient
)

type Stdin struct {
	hash string
	path string
}

func main() {
	// Load config before building the command tree so `import` can generate a
	// subcommand per configured dir-type. The -c/--config flag isn't parsed yet
	// at this point, so peek it out of os.Args directly.
	config, err := client.LoadConfig(configArg(os.Args))
	if err != nil {
		log.Println(err)
		log.Println("Error opening config file!")
	} else {
		tie = client.NewTieClient(config)
	}

	cmd := &cli.Command{
		Name:                  "tie",
		Usage:                 "Hey",
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "Config file to load", Value: "config.toml"},
			&cli.StringFlag{Name: "table", Aliases: []string{"t"}, Usage: "Output as table"},
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
		log.Fatal(err)
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

// if err := app.Run(os.Args); err != nil {
// 	log.Fatal(err)
// }
// fi, err := os.Stdin.Stat()
// if err != nil {
// 	panic(err)
// }
// if !(fi.Mode()&os.ModeNamedPipe == 0) {
// 	scanner := bufio.NewScanner(os.Stdin)
// 	first := true
// 	for scanner.Scan() {
// 		line := scanner.Text()
// 		if first && len(line) > 1 && line[0] == '{' { // json input

// 		} else {
// 			parts := strings.Split(line, "\t")
// 			// Assumes input from put
// 			if len(parts) == 2 {
// 				input := Stdin{parts[0], parts[1]}
// 				stdin = append(stdin, input)
// 			} else {
// 				input := Stdin{hash: line}
// 				stdin = append(stdin, input)
// 			}
// 		}
// 	}

// 	if err := scanner.Err(); err != nil {
// 		log.Println(err)
// 	}
// }
// cobra.OnInitialize(func() {
// 	config := client.ReadConfig(configFile)
// 	tie = client.NewTieClient(config)
// })

// var rootCmd = &cobra.Command{Use: "tie"}
// rootCmd.AddCommand(cmdList()...)
// rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "config", "Config file to load")
// // rootCmd.PersistentFlags().BoolVarP(&tie.CurrentState.Verbose, "verbose", "v", false, "Verbose output")
// rootCmd.PersistentFlags().BoolVarP(&outputAsTable, "table", "t", false, "Output as table")

// rootCmd.Execute()
