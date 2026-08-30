package client

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/uidbz/conf"
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

// ConfigFileName lets callers omit the extension: `tie -c myconf` resolves to
// myconf.toml. A name that already ends in .toml is left as-is.
func ConfigFileName(configName string) string {
	if configName != "" && !strings.HasSuffix(configName, ".toml") {
		return configName + ".toml"
	}
	return configName
}

func LoadConfig(configName string) (Config, error) {
	configName = ConfigFileName(configName)
	c := Config{}
	loadPath, err := conf.LoadConfig("tie", configName, &c)
	if err != nil {
		// Preserve os.IsNotExist detection for callers, but name the file on a
		// parse error so a stale/legacy config is easy to find and fix.
		if !os.IsNotExist(err) {
			err = fmt.Errorf("parsing %s: %w", loadPath, err)
		}
		return defaultConfig, err
	}
	c.configPath = loadPath

	return c, nil
}

// LoadOrCreateConfig loads configName like LoadConfig, but when no config file
// exists in any of the searched locations it writes a default one to the user
// config dir and loads that instead. A file that exists but fails to parse is
// still reported as an error (so a stale/broken config is never overwritten).
// The returned bool reports whether a new default was created.
func LoadOrCreateConfig(configName string) (Config, bool, error) {
	c, err := LoadConfig(configName)
	if err == nil {
		return c, false, nil
	}
	// Only auto-create when the file is genuinely absent everywhere; a parse
	// error means a config exists and must not be clobbered.
	if !os.IsNotExist(err) {
		return c, false, err
	}
	if err := SaveConfig(configName, defaultConfig); err != nil {
		return defaultConfig, false, err
	}
	created, err := LoadConfig(configName)
	if err != nil {
		return defaultConfig, false, err
	}
	return created, true, nil
}

func SaveConfig(name string, config Config) error {
	return conf.SaveToUserConfigDir("tie", ConfigFileName(name), config)
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
