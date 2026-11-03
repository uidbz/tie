package main

import (
	"testing"

	"github.com/uidbz/tie/auth"
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

	// A ReadOnly from the TIE config survives folding: after applyDirectConfig
	// tieCfg.ReadOnly is the effective state, which is what the rest of the
	// startup must consult (#153).
	tieCfg = client.Config{TripleStoreFile: "/from/tie/Main.tie", ReadOnly: true}
	applyDirectConfig(&tieCfg, SMBConfig{})
	if !tieCfg.ReadOnly {
		t.Errorf("tie-config ReadOnly was dropped: %+v", tieCfg)
	}
}

// TestEffectiveRoleDowngradesWriters pins #153: with the database open
// read-only — from tie-smb's flag OR the tie config — every session is
// read-only; handing out a write role only moves the refusal to the database.
func TestEffectiveRoleDowngradesWriters(t *testing.T) {
	if got := effectiveRole(auth.RoleWrite, true); got != auth.RoleRead {
		t.Errorf("write role under read-only db = %v, want read", got)
	}
	if got := effectiveRole(auth.RoleWrite, false); got != auth.RoleWrite {
		t.Errorf("write role under a writable db = %v, want write", got)
	}
	if got := effectiveRole(auth.RoleRead, true); got != auth.RoleRead {
		t.Errorf("read role = %v, want read", got)
	}
}

// TestShareOptionsForEmptyWriterList pins #153: smb's WriteUsers() with no
// names means every connection may write, so an all-read-only user list must
// export the share itself read-only instead of advertising write access.
func TestShareOptionsForEmptyWriterList(t *testing.T) {
	opts, mode := shareOptionsFor(nil)
	if mode != "read-only" {
		t.Errorf("empty writer list mode = %q, want read-only", mode)
	}
	if len(opts) != 1 {
		t.Fatalf("empty writer list options = %d, want 1 (ReadOnly export)", len(opts))
	}
	opts, mode = shareOptionsFor([]string{"alice", "bob"})
	if mode != "alice,bob" || len(opts) != 1 {
		t.Errorf("writer list = %q / %d opts, want alice,bob via WriteUsers", mode, len(opts))
	}
}
