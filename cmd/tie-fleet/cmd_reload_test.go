package main

import (
	"testing"

	runnerapi "github.com/uidbz/tie/runner/api"
)

func TestSupportsReload(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v1.2.0", true},
		{"v1.2.0-3-gabcdef", true},
		{"v1.3.0", true},
		{"v2.0.0", true},
		{"v0.7.0", true},  // the tie-runner line has had reload from the start
		{"master", false}, // unversioned dev builds: refuse to be blind
		{"v1.1.0", false}, // these die on SIGHUP
		{"v1.0.0", false},
		{"dev", false},
		{"unknown", false},
		{"", false},
	}
	for _, c := range cases {
		if got := supportsReload(runnerapi.CapabilityReply{Version: c.version}); got != c.want {
			t.Errorf("supportsReload(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}

func TestSupportsAdoption(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v1.6.0", true},
		{"v1.6.1", true},
		{"v1.6.0-3-gabcdef", true},
		{"v2.0.0", true},
		{"v0.7.0", true},
		{"v1.5.5", false}, // the early 1.x line recreates services
		{"v1.5.4", false},
		{"master", false},
		{"dev", false},
	}
	for _, c := range cases {
		if got := supportsAdoption(runnerapi.CapabilityReply{Version: c.version}); got != c.want {
			t.Errorf("supportsAdoption(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}
