package main

import (
	"bufio"
	"log"
	"os"
	"strings"

	"git.sr.ht/~uid/tie/client"
	"github.com/spf13/cobra"
)

var (
	stdin         []Stdin
	outputAsTable bool
)

type Stdin struct {
	hash string
	path string
}

func main() {
	fi, err := os.Stdin.Stat()
	if err != nil {
		panic(err)
	}
	if !(fi.Mode()&os.ModeNamedPipe == 0) {
		scanner := bufio.NewScanner(os.Stdin)
		first := true
		for scanner.Scan() {
			line := scanner.Text()
			if first && len(line) > 1 && line[0] == '{' { // json input

			} else {
				parts := strings.Split(line, "\t")
				// Assumes input from put
				if len(parts) == 2 {
					input := Stdin{parts[0], parts[1]}
					stdin = append(stdin, input)
				} else {
					input := Stdin{hash: line}
					stdin = append(stdin, input)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Println(err)
		}
	}
	cobra.OnInitialize(tie.InitConfig)

	var rootCmd = &cobra.Command{Use: "tie"}
	rootCmd.AddCommand(cmdList()...)
	rootCmd.PersistentFlags().StringVarP(&tie.Config, "config", "c", "config", "Config file to load")
	rootCmd.PersistentFlags().BoolVarP(&tie.CurrentState.Verbose, "verbose", "v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVarP(&outputAsTable, "table", "t", false, "Output as table")

	rootCmd.Execute()
}
