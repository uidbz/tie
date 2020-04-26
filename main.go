package main

import (
	"log"
	"os"
	"os/user"

	"github.com/spf13/cobra"
)

var (
	state      State
	config     string
	configDir  string
	configPath string
	verbose    bool
)

func main() {
	cobra.OnInitialize(initConfig)

	var rootCmd = &cobra.Command{Use: "app"}
	rootCmd.AddCommand(cmdList()...)
	rootCmd.PersistentFlags().StringVarP(&config, "config", "c", "config", "Config file to load")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

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
		state.Webservice = "http://localhost:8080"
		state.Namespace = "Collections"
		state.Collection = "Main"

		SaveJSON(configPath, state)
		PrintState()
	} else {
		LoadJSON(configPath, &state)
		PrintState()
	}
}
