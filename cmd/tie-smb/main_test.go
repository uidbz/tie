package main

import (
	"testing"

	"github.com/uidbz/tie/client"
)

// TestApplyDirectConfig: the tie-smb config can point the service at a local
// .tie file (and open it read-only) without editing the shared tie config.
func TestApplyDirectConfig(t *testing.T) {
	tieCfg := client.Config{TripleStoreURL: "http://store:1161"}
	applyDirectConfig(&tieCfg, SMBConfig{})
	if tieCfg.TripleStoreFile != "" || tieCfg.ReadOnly {
		t.Errorf("empty overrides changed the config: %+v", tieCfg)
	}

	applyDirectConfig(&tieCfg, SMBConfig{TripleStoreFile: "/data/Collections/Main.tie", ReadOnly: true})
	if tieCfg.TripleStoreFile != "/data/Collections/Main.tie" || !tieCfg.ReadOnly {
		t.Errorf("overrides not applied: %+v", tieCfg)
	}

	// A tie config's own file survives when tie-smb does not override it.
	tieCfg = client.Config{TripleStoreFile: "/from/tie/Main.tie"}
	applyDirectConfig(&tieCfg, SMBConfig{})
	if tieCfg.TripleStoreFile != "/from/tie/Main.tie" {
		t.Errorf("tie config file was lost: %+v", tieCfg)
	}
}
