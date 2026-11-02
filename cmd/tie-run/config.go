package main

// The synced client-side config at <UserConfigDir>/tie/tie-run-config.toml:
// it maps a deployed image name to the runner that hosts it and the
// DeployKey needed to manage (cancel jobs for, stop, or delete) that
// image. Deploys write an entry as they rotate the key, and after that
// the management commands work from any directory without re-reading the
// project file. TIE_RUN_CONFIG overrides the location (used by tests;
// also handy for running against a scratch config).

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

type TieRunConfig struct {
	Images map[string]ImageEntry
}

type ImageEntry struct {
	Runner    string
	DeployKey string
}

// tieRunConfigPath returns the config location:
// <UserConfigDir>/tie on every platform (on Windows that is
// %APPDATA%\tie, not the machine-wide C:\tie).
func tieRunConfigPath() (string, error) {
	if env := os.Getenv("TIE_RUN_CONFIG"); env != "" {
		return env, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config directory: %w", err)
	}
	return filepath.Join(configDir, "tie", "tie-run-config.toml"), nil
}

// loadTieRunConfig reads the config, returning an empty (non-nil) config
// if the file does not yet exist.
func loadTieRunConfig() (TieRunConfig, string, error) {
	path, err := tieRunConfigPath()
	if err != nil {
		return TieRunConfig{}, "", err
	}
	cfg := TieRunConfig{Images: map[string]ImageEntry{}}
	if _, err := os.Stat(path); err == nil {
		b, err := os.ReadFile(path)
		if err != nil {
			return TieRunConfig{}, path, fmt.Errorf("failed to read %s: %w", path, err)
		}
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return TieRunConfig{}, path, fmt.Errorf("failed to load %s: %w", path, err)
		}
	}
	if cfg.Images == nil {
		cfg.Images = map[string]ImageEntry{}
	}
	return cfg, path, nil
}

// saveConfigAtomic marshals v to TOML and replaces path atomically (temp
// file in the same directory + rename) with mode 0600. Config files hold
// DeployKeys; a 0644 mode would leave them world-readable. The parent
// directory is created 0700.
func saveConfigAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	b, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// saveTieRunConfig writes the config atomically with mode 0600, creating
// the directory if needed.
func saveTieRunConfig(cfg TieRunConfig, path string) error {
	if err := saveConfigAtomic(path, cfg); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// mirrorDeployKey records the image's runner + DeployKey in the config so
// management commands (cancel, services, delete) can authorize right
// after a deploy.
func mirrorDeployKey(name, runner, deployKey string) error {
	cfg, path, err := loadTieRunConfig()
	if err != nil {
		return err
	}
	cfg.Images[name] = ImageEntry{Runner: runner, DeployKey: deployKey}
	return saveTieRunConfig(cfg, path)
}
