// Package version exposes the build version shared by all tie binaries.
package version

import "runtime/debug"

// Version is the build version. It is overridden at build time via
//
//	-ldflags "-X github.com/uidbz/tie/version.Version=v0.4.0"
//
// (see the Makefile). When built without that flag — e.g. `go install` from a
// tagged module — it falls back to the version baked into the build info.
var Version = "dev"

// String returns the build version, preferring an ldflags-injected value and
// otherwise the module version recorded in the binary's build info.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
