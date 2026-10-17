package client

import (
	"fmt"
	"os"
	"path/filepath"
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

// Testing URLs are package vars so the test environment (testenv_test.go)
// can point them at in-process servers; outside `go test` they keep the
// test-env defaults.
var (
	testingTripleStoreURL = "http://localhost:2161"
	testingFileHostURL    = "http://localhost:2162"
)

func TestingConfig() Config {
	config := defaultConfig
	config.Namespace = "testing"
	config.Collection = "testing"
	config.TripleStoreURL = testingTripleStoreURL
	config.FileHosts = map[string]FileHost{"default": {URL: testingFileHostURL}}
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

// isConfigPath reports whether name should be read from that exact filesystem
// path rather than searched by name in the cwd + app config dirs. Any value that
// is absolute or contains a path separator is a path; a bare name keeps the
// conf.LoadConfig search behavior. Mirrors how tie-gui branches on the -config
// value (see cmd/tie-view loadTieConfig).
func isConfigPath(name string) bool {
	return filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/')
}

func LoadConfig(configName string) (Config, error) {
	configName = ConfigFileName(configName)
	c := Config{}
	var loadPath string
	var err error
	if isConfigPath(configName) {
		// An explicit path (absolute or containing a separator) is read directly
		// so `tie -C /abs/path/config.toml` loads that file instead of treating
		// the whole path as a name searched under the config dir.
		loadPath = configName
		err = conf.ReadConfig(loadPath, &c)
	} else {
		loadPath, err = conf.LoadConfig("tie", configName, &c)
	}
	if err != nil {
		// Preserve os.IsNotExist detection for callers, but name the file on a
		// parse error so a stale/legacy config is easy to find and fix.
		if !os.IsNotExist(err) {
			err = fmt.Errorf("parsing %s: %w", loadPath, err)
		}
		return defaultConfig, err
	}
	c.configPath = loadPath
	normalizeConfig(&c)

	return c, nil
}

// normalizeConfig applies backward-compatible defaults after decode: the
// deprecated Webservice field maps to TripleStoreURL (and vice versa).
//
// A config with no [Collections] is deliberately left without one. Synthesizing
// an entry from the flat Namespace/Collection fields would make that entry
// shadow the fields it was copied from, so a caller that loads a config and then
// overrides Config.Namespace/Config.Collection (the documented way to target a
// collection programmatically) would be silently ignored. ResolveCollection's
// unnamed path already resolves the flat fields identically.
func normalizeConfig(c *Config) {
	if c.TripleStoreURL == "" {
		c.TripleStoreURL = c.Webservice
	}
	if c.Webservice == "" {
		c.Webservice = c.TripleStoreURL
	}
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
	_, err := SaveConfigTo(name, config)
	return err
}

// SaveConfigTo writes the config and reports where it landed. `tie conf create`
// is the only thing that writes a config (§6.6), so it has to be able to say
// where the file went — and where it went follows the same rule as loading:
// an explicit path (what -C or $TIE_CONFIG names) is written there, a bare name
// goes to the user config dir.
func SaveConfigTo(name string, config Config) (string, error) {
	name = ConfigFileName(name)
	if isConfigPath(name) {
		if err := conf.WriteConfig(name, config); err != nil {
			return "", err
		}
		return name, nil
	}
	if err := conf.SaveToUserConfigDir("tie", name, config); err != nil {
		return "", err
	}
	return conf.PathUserConfigDir("tie", name)
}
