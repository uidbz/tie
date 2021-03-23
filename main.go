package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"git.sr.ht/~uid/tie-client"
	"github.com/spf13/cobra"
)

var (
	// state      State
	config     string
	configDir  string
	configPath string
	hashRoot   = "/data/"
	key        []byte
	stdin      []Stdin
)

type Stdin struct {
	hash string
	path string
}

const (
	tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
)

func InitKey() {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		fmt.Printf("Cannot decode hex key: %v", err) // add error handling
		return
	}
	key = k
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
				if len(parts) == 2 {
					input := Stdin{parts[0], parts[1]}
					stdin = append(stdin, input)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Println(err)
		}
	}
	InitKey()
	cobra.OnInitialize(initConfig)

	var rootCmd = &cobra.Command{Use: "app"}
	rootCmd.AddCommand(cmdList()...)
	rootCmd.PersistentFlags().StringVarP(&config, "config", "c", "config", "Config file to load")
	rootCmd.PersistentFlags().BoolVarP(&tie.CurrentState.Verbose, "verbose", "v", false, "Verbose output")

	rootCmd.Execute()
}

func initConfig() {
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	configDir = filepath.Join(usr.HomeDir, ".config", "tie")
	configPath = configDir + "/" + config + ".json"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if _, err2 := os.Stat(configDir); os.IsNotExist(err2) {
			if os.Mkdir(configDir, 0777) != nil {
				panic("Can't create " + configDir + "\nExiting...")
			}
		}
		s := tie.State{
			Webservice: "https://localhost:1161",
			Namespace:  "Collections",
			Collection: "Main",
		}
		tie.CurrentState = s

		SaveJSON(configPath, tie.CurrentState)
		tie.PrintState()
	} else {
		LoadJSON(configPath, &tie.CurrentState)
		tie.PrintState()
	}
}
