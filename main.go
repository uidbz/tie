package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/user"

	"github.com/spf13/cobra"
	"nicecode.rocks/uid/tie-client"
)

var (
	// state      State
	config     string
	configDir  string
	configPath string
	hashRoot   = "/data/"
	key        []byte
)

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
	configDir = usr.HomeDir + "/.config/tie"
	configPath = configDir + "/" + config + ".json"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// path/to/whatever does not exist
		if a, b := Exists(configDir); !a && b != nil {
			if os.Mkdir(configDir, 0777) != nil {
				panic("Can't create " + configDir + "\nExiting...")
			}
		}
		s := tie.State{
			Webservice: "http://localhost:8080",
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
