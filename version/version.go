// Package version exposes the build version shared by all tie binaries.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Version is the build version. It is overridden at build time via
//
//	-ldflags "-X github.com/uidbz/tie/version.Version=v0.4.0"
//
// (see the Makefile, which injects `git describe --tags --always --dirty`).
// When built without that flag the binary's embedded build info is used
// instead: the module version for a `go install pkg@version` build, else the
// VCS commit Go records for any build from a git checkout.
var Version = "dev"

// Info describes the running build.
type Info struct {
	// Version is the human-readable version: a tag (v0.5.2), a git description
	// (v0.5.2-3-g174d68f), a module pseudo-version, or a bare short commit —
	// suffixed "-dirty" when built from a modified tree without ldflags.
	Version string `json:"version"`
	// Commit is the full VCS revision when the build info carries one.
	Commit string `json:"commit,omitempty"`
	// Date is the commit timestamp (RFC 3339) when known.
	Date string `json:"date,omitempty"`
	// Dirty reports uncommitted changes in the tree the binary was built from.
	Dirty bool `json:"dirty,omitempty"`
	// GoVersion is the toolchain that built the binary.
	GoVersion string `json:"goVersion"`
}

// String returns the build version, preferring an ldflags-injected value, then
// the module version from build info, then the embedded VCS commit.
func String() string {
	return Get().Version
}

// Get returns the full build description. It never fails: fields that the
// build did not record are left empty and Version falls back to "dev".
func Get() Info {
	info := Info{Version: Version, GoVersion: runtime.Version()}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.Date = s.Value
		case "vcs.modified":
			info.Dirty = s.Value == "true"
		}
	}
	if Version != "dev" {
		// ldflags already describe the tree (git describe --dirty), so do not
		// re-append the VCS dirty marker.
		return info
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		info.Version = v
		return info
	}
	if info.Commit != "" {
		info.Version = shortCommit(info.Commit)
		if info.Dirty {
			info.Version += "-dirty"
		}
	}
	return info
}

func shortCommit(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return strings.TrimSpace(rev)
}
