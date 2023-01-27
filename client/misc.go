package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

func DefaultConfig() Config {
	return ReadConfig(defaultConfigFile)
}

func TestingConfig() Config {
	config := ReadConfig(defaultConfigFile)
	config.Namespace = "testing"
	config.Collection = "testing"
	return config
}

func ReadConfig(configFile string) Config {
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	if configDir, err := os.Getwd(); err == nil {
		config.configDir = configDir
		config.configPath = config.configDir + "/" + configFile + ".json"
	}
	// If config file does not exist in workdir, then assume config will be in $HOME/.config/tie
	if _, err := os.Stat(config.configPath); os.IsNotExist(err) {
		config.configDir = filepath.Join(usr.HomeDir, ".config", "tie")
		config.configPath = config.configDir + "/" + configFile + ".json"

		if _, err := os.Stat(config.configPath); os.IsNotExist(err) {
			if _, err2 := os.Stat(config.configDir); os.IsNotExist(err2) {
				if os.MkdirAll(config.configDir, 0777) != nil {
					panic("Can't create " + config.configDir + "\nExiting...")
				}
			}
			return config // return default values
		}
	}
	LoadJSON(config.configPath, &config)

	return config
}

func LoadJSON(inputFile string, dest interface{}) bool {
	file, err := os.Open(inputFile)

	if err != nil {
		log.Fatal(err)
		return false
	} else {
		input, err2 := io.ReadAll(file)
		if err2 != nil {
			log.Fatal(err2)
			return false
		}
		if err := json.Unmarshal(input, &dest); err != nil {
			return false
		}
		return true

	}
}

func SaveJSON(outputFile string, source interface{}) bool {
	b, _ := json.Marshal(source)
	err := os.WriteFile(outputFile, b, 0766)
	if err != nil {
		log.Fatal(err)
		return false
	} else {
		return true
	}
}

func (tc *TieClient) PrintState() {
	if tc.Config.Verbose {
		fmt.Println("Using host:", tc.Config.Webservice)
		fmt.Println("Current namespace/collection is " + tc.Config.Namespace + "/" + tc.Config.Collection)
	}
}

func InitKey() []byte {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		fmt.Printf("Cannot decode hex key: %v", err) // add error handling
		return nil
	}

	return k
}
func GetVideoHeight(source string) string {
	//ffprobe -v error -select_streams v:0 -show_entries stream=height -of default=nw=1:nk=1
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=height",
		"-of", "default=noprint_wrappers=1:nokey=1",
		source,
	}
	cmd := exec.Command("ffprobe", args...)
	if out, err := cmd.Output(); err != nil {
		fmt.Println(err)
		return ""
	} else {
		return strings.TrimSpace(string(out)) + "p"
	}
}
