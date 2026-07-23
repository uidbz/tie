package client

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"

	"git.sr.ht/~uid/conf"
)

func DefaultConfig() Config {
	conf, err := LoadConfig("config.toml")
	if err != nil {
		return defaultConfig
	}
	return conf
}

func TestingConfig() Config {
	config := defaultConfig
	config.Namespace = "testing"
	config.Collection = "testing"
	config.Webservice = "http://localhost:1161"
	config.FileHosts = map[string]FileHost{"default": {URL: "http://localhost:1162"}}
	return config
}

// Deprecated - use LoadConfig instead
func ReadConfig(configName string) Config {
	c, _ := LoadConfig(configName)
	return c
}

func LoadConfig(configName string) (Config, error) {
	c := Config{}
	loadPath, err := conf.LoadConfig("tie", configName, &c)
	if err != nil {
		return defaultConfig, err
	}
	c.configPath = loadPath

	return c, nil
}

func SaveConfig(name string, config Config) error {
	return conf.SaveToUserConfigDir("tie", name, config)
}

func (tc *TieClient) PrintState() {
	if tc.Config.verbose {
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
