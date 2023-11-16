package main

import (
	"log"
	"os"

	"git.sr.ht/~uid/tie/client"

	// "github.com/spf13/cobra"
	"github.com/urfave/cli/v2"
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
	app := &cli.App{
		Usage: "Hey",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "Config file to load", Value: "config"},
			&cli.StringFlag{Name: "table", Aliases: []string{"t"}, Usage: "Output as table"},
		},
		Commands: []*cli.Command{
			cmdAdd(),
			cmdDel(),
			cmdGet(),
			cmdConf(),
			cmdImport(),
		},
		Before: func(cCtx *cli.Context) error {
			config, err := client.LoadConfig(cCtx.String("config"))
			if err != nil {
				log.Fatal(err)
			}
			tie = client.NewTieClient(config)
			return nil
		},
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
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
