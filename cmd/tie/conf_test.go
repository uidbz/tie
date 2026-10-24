package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
)

// appLoading mirrors what main does with a config name: load it, and remember
// why it could not be loaded rather than writing a default.
func appLoading(configName string) *app {
	a := &app{}
	if cfg, err := client.LoadConfig(configName); err != nil {
		a.configErr = err
	} else {
		a.config = cfg
		a.tie = client.NewTieClientFor(cfg, "")
	}
	return a
}

// TestMissingConfigIsNotWritten: every invocation used to leave a config file
// behind in the user config dir — `tie --help`, a typo, a read-only query. Only
// `tie conf create` writes one now (§6.6).
func TestMissingConfigIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "config.toml")

	_, _, code, runErr := runTie(t, appLoading(missing), "", "-C", missing, "get", "anything")
	if code != exitUsage {
		t.Errorf("a data command without a config exited %d, want %d", code, exitUsage)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "conf create") {
		t.Errorf("the refusal does not say how to fix it: %v", runErr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("the failed run created %s", missing)
	}

	// Help and version still work with no config at all.
	if _, _, code, _ := runTie(t, appLoading(missing), "", "-C", missing, "version"); code != 0 {
		t.Errorf("`tie version` without a config exited %d, want 0", code)
	}
	if _, _, code, _ := runTie(t, appLoading(missing), "", "-C", missing, "--help"); code != 0 {
		t.Errorf("`tie --help` without a config exited %d, want 0", code)
	}

	// conf create is the one command that writes, and it writes where -C says.
	if _, _, code, _ := runTie(t, appLoading(missing), "", "-C", missing, "conf", "create"); code != 0 {
		t.Fatalf("`tie conf create` exited %d", code)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Errorf("`tie conf create` did not write %s: %v", missing, err)
	}
}

// TestTIEConfigEnv: $TIE_CONFIG is the conventional override, and -C still wins
// over it (§6.6).
func TestTIEConfigEnv(t *testing.T) {
	dir := t.TempDir()
	fromEnv := filepath.Join(dir, "env.toml")
	fromFlag := filepath.Join(dir, "flag.toml")

	t.Setenv("TIE_CONFIG", fromEnv)
	if name, _, _, _ := parseGlobalFlags([]string{"tie"}); name != fromEnv {
		t.Errorf("TIE_CONFIG was ignored: got %q", name)
	}
	if name, _, _, _ := parseGlobalFlags([]string{"tie", "-C", fromFlag, "get", "x"}); name != fromFlag {
		t.Errorf("-C did not override TIE_CONFIG: got %q", name)
	}

	t.Setenv("TIE_CONFIG", "")
	if name, _, _, _ := parseGlobalFlags([]string{"tie"}); name != "config.toml" {
		t.Errorf("without TIE_CONFIG the default should be config.toml, got %q", name)
	}
	// A local flag on a subcommand is not the global one.
	if _, collection, _, _ := parseGlobalFlags([]string{"tie", "verify", "-c", "Other"}); collection != "" {
		t.Errorf("a subcommand's -c leaked into the global collection: %q", collection)
	}
}

// TestConfShowIsTheResolvedState: config resolution has enough layers that the
// only way to see what an invocation means is to ask it (§9).
func TestConfShowIsTheResolvedState(t *testing.T) {
	a := newTestApp(t)
	a.config = client.Config{
		TripleStoreURL:   "http://store.example:1161",
		Namespace:        "Collections",
		Collection:       "Main",
		Username:         "alice",
		Password:         "hunter2",
		DefaultFileHosts: []string{"default"},
		FileHosts: map[string]client.FileHost{
			"default": {URL: "http://blobs.example:1162"},
			"backup":  {URL: "https://backup.example"},
		},
		DefaultCollection: "Main",
		Collections: map[string]client.CollectionEntry{
			"Archive": {Namespace: "Collections", Collection: "Archive2024", TripleStoreURL: "http://archive.example:1161"},
		},
		ImportDest:   map[string]string{"audio-dir": "/music/{artist}/{album}"},
		PrevVersions: 3,
	}
	a.tie = client.NewTieClientFor(a.config, "")

	stdout, _, code, _ := runTie(t, a, "", "conf", "show", "-c", "Archive")
	if code != 0 {
		t.Fatalf("conf show exited %d", code)
	}

	want := map[string]string{
		"collection":            "Archive2024",
		"triplestore":           "http://archive.example:1161",
		"filehost:backup":       "https://backup.example",
		"import-dest:audio-dir": "/music/{artist}/{album}",
		"prev-versions":         "3",
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			t.Fatalf("row %q is not two fields", line)
		}
		got[fields[0]] = fields[1]
	}
	for field, value := range want {
		if got[field] != value {
			t.Errorf("%s = %q, want %q (all: %v)", field, got[field], value, got)
		}
	}
	if strings.Contains(stdout, "hunter2") {
		t.Errorf("the password was printed:\n%s", stdout)
	}
}

// TestConfHasNoShortAlias: `c` read like the global -c (collection), so
// `tie c create` was a trap (§6.6). Checked on the tree rather than by running
// it, because cli's unknown-command path exits the process.
func TestConfHasNoShortAlias(t *testing.T) {
	a := newTestApp(t)
	root := newRootCommand(a)
	for _, sub := range root.Commands {
		if sub.Name == "conf" {
			if len(sub.Aliases) != 0 {
				t.Errorf("conf still has aliases %v; the `c` alias is dropped", sub.Aliases)
			}
			return
		}
	}
	t.Error("no conf command in the tree")
}
