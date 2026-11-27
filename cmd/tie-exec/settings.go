package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/metadata"
)

// maxSettingsBytes bounds a settings blob fetched by hash.
const maxSettingsBytes = 16 << 20

// resolveSettings returns the settings file to hand the app, in order of
// precedence: -settings FILE; -settings-hash / $TIE_SETTINGS_HASH (fetched
// from the filehost); inline JSON in $TIE_SETTINGS. Fetched or inline
// settings are written to a temp file that cleanup removes. "" means no
// settings.
func resolveSettings(app externalApp, getenv func(string) string) (path string, cleanup func(), err error) {
	cleanup = func() {}
	if app.settings != "" {
		return app.settings, cleanup, nil
	}
	var data []byte
	hash := app.settingsHash
	if hash == "" {
		hash = getenv("TIE_SETTINGS_HASH")
	}
	switch {
	case hash != "":
		if !metadata.IsHexHash(hash) {
			return "", cleanup, fmt.Errorf("settings hash %q is not a content address", hash)
		}
		body, err := fhclient.New(app.filehost, nil).Get(hash)
		if err != nil {
			return "", cleanup, fmt.Errorf("fetching settings %s: %w", hash, err)
		}
		defer body.Close()
		data, err = io.ReadAll(io.LimitReader(body, maxSettingsBytes+1))
		if err != nil {
			return "", cleanup, fmt.Errorf("fetching settings %s: %w", hash, err)
		}
		if len(data) > maxSettingsBytes {
			return "", cleanup, fmt.Errorf("settings %s exceed %d bytes", hash, maxSettingsBytes)
		}
	case getenv("TIE_SETTINGS") != "":
		data = []byte(getenv("TIE_SETTINGS"))
	default:
		return "", cleanup, nil
	}
	if !json.Valid(data) {
		return "", cleanup, fmt.Errorf("settings are not valid JSON")
	}
	f, err := os.CreateTemp("", "tie-settings-*.json")
	if err != nil {
		return "", cleanup, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", cleanup, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", cleanup, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}
