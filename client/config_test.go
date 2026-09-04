package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfigAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	toml := "TripleStoreURL = \"https://box\"\nNamespace = \"NS\"\nCollection = \"Main\"\n"
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%q) failed: %v", path, err)
	}
	if c.Path() != path {
		t.Errorf("Path() = %q, want %q", c.Path(), path)
	}
	if c.TripleStoreURL != "https://box" || c.Namespace != "NS" {
		t.Errorf("loaded config wrong: %+v", c)
	}
	// normalizeConfig must run for the path branch too: a synthesized default
	// collection resolving to the file's connection.
	if got := c.ResolveCollection(""); got.TripleStoreURL != "https://box" || got.Collection != "Main" {
		t.Errorf("resolved default wrong: %+v", got)
	}
}

func TestLoadConfigPathAddsTomlExtension(t *testing.T) {
	dir := t.TempDir()
	// The .toml convenience must apply on the path branch: naming "custom" loads
	// custom.toml sitting next to it.
	if err := os.WriteFile(filepath.Join(dir, "custom.toml"), []byte("Namespace = \"X\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(filepath.Join(dir, "custom"))
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if c.Namespace != "X" {
		t.Errorf("Namespace = %q, want X", c.Namespace)
	}
}

func TestLoadConfigAbsentPathIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	_, err := LoadConfig(path)
	if !os.IsNotExist(err) {
		t.Errorf("absent path should report os.IsNotExist, got %v", err)
	}
}

func TestLoadOrCreateConfigAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created.toml")
	c, created, err := LoadOrCreateConfig(path)
	if err != nil {
		t.Fatalf("LoadOrCreateConfig failed: %v", err)
	}
	if !created {
		t.Error("expected created = true for a missing path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("config was not written to %q: %v", path, statErr)
	}
	if c.Path() != path {
		t.Errorf("Path() = %q, want %q", c.Path(), path)
	}
}

func TestResolveCollectionFallbacks(t *testing.T) {
	c := Config{
		Username:         "top",
		Password:         "toppw",
		Namespace:        "TopNS",
		Collection:       "topcoll",
		TripleStoreURL:   "https://main",
		DefaultFileHosts: []string{"media"},
		Collections: map[string]CollectionEntry{
			"images": {Namespace: "Pics", Collection: "images", FileHosts: []string{"ssd"}},
			"archive": {
				TripleStoreURL: "https://archive-box",
				Namespace:      "Cold",
				Username:       "arch",
				Password:       "archpw",
			},
		},
	}

	got := c.ResolveCollection("images")
	want := ResolvedCollection{
		Namespace:      "Pics",
		Collection:     "images",
		TripleStoreURL: "https://main", // inherited from top-level
		Username:       "top",          // inherited
		Password:       "toppw",
		FileHosts:      []string{"ssd"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("images:\n got %+v\nwant %+v", got, want)
	}

	got = c.ResolveCollection("archive")
	want = ResolvedCollection{
		Namespace:      "Cold",
		Collection:     "archive", // falls back to the entry name
		TripleStoreURL: "https://archive-box",
		Username:       "arch", // entry creds used as a pair
		Password:       "archpw",
		FileHosts:      []string{"media"}, // inherited DefaultFileHosts
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("archive:\n got %+v\nwant %+v", got, want)
	}
}

func TestResolveCollectionUnknownNameIsBareID(t *testing.T) {
	c := Config{Namespace: "NS", Collection: "def", TripleStoreURL: "https://d"}
	got := c.ResolveCollection("adhoc")
	if got.Collection != "adhoc" || got.Namespace != "NS" || got.TripleStoreURL != "https://d" {
		t.Errorf("unknown name should be a bare collection id on the top-level triplestore: %+v", got)
	}
}

func TestResolveHosts(t *testing.T) {
	c := Config{
		Namespace:         "Collections",
		Collection:        "Main",
		TripleStoreURL:    "https://main",
		DefaultFileHosts:  []string{"top"},
		DefaultCollection: "Main",
		Collections: map[string]CollectionEntry{
			"Main":  {Namespace: "Collections", Collection: "Main"},
			"media": {Collection: "media", FileHosts: []string{"ssd", "hdd"}},
		},
	}

	// An explicit --host list always wins, over any collection binding.
	if got := NewTieClient(c).ResolveHosts("", []string{"explicit"}); !reflect.DeepEqual(got, []string{"explicit"}) {
		t.Errorf("explicit hosts: got %v", got)
	}
	// The default collection has no FileHosts of its own: inherit the top-level.
	if got := NewTieClient(c).ResolveHosts("", nil); !reflect.DeepEqual(got, []string{"top"}) {
		t.Errorf("default collection should inherit DefaultFileHosts, got %v", got)
	}
	// A client bound to a collection with its own FileHosts (tie -c media)
	// resolves those, overriding the top-level default.
	if got := NewTieClientFor(c, "media").ResolveHosts("", nil); !reflect.DeepEqual(got, []string{"ssd", "hdd"}) {
		t.Errorf("bound collection FileHosts should override DefaultFileHosts, got %v", got)
	}
	// An explicit collection name (import --collection media) steers hosts too,
	// even on a client bound to another collection.
	if got := NewTieClient(c).ResolveHosts("media", nil); !reflect.DeepEqual(got, []string{"ssd", "hdd"}) {
		t.Errorf("named collection FileHosts should override DefaultFileHosts, got %v", got)
	}
}

func TestNormalizeConfigLegacy(t *testing.T) {
	c := Config{
		Webservice:       "https://legacy",
		Namespace:        "Collections",
		Collection:       "Main",
		DefaultFileHosts: []string{"default"},
	}
	normalizeConfig(&c)

	if c.TripleStoreURL != "https://legacy" {
		t.Errorf("Webservice should populate TripleStoreURL, got %q", c.TripleStoreURL)
	}
	// A flat config gains no [Collections] entry: one synthesized from the flat
	// fields would shadow them (see TestFlatFieldOverrideIsHonored).
	if len(c.Collections) != 0 || c.DefaultCollection != "" {
		t.Errorf("flat config should stay flat, got DefaultCollection=%q Collections=%+v",
			c.DefaultCollection, c.Collections)
	}

	// It must still resolve to the legacy connection, filehosts included.
	got := c.ResolveCollection("")
	if got.TripleStoreURL != "https://legacy" || got.Collection != "Main" || got.Namespace != "Collections" {
		t.Errorf("resolved default from legacy config wrong: %+v", got)
	}
	if !reflect.DeepEqual(got.FileHosts, []string{"default"}) {
		t.Errorf("resolved filehosts wrong: %+v", got.FileHosts)
	}
}

// A caller that loads a config and then retargets it — what every embedder does
// to address its own namespace instead of the user's default — must be honored.
func TestFlatFieldOverrideIsHonored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user.toml")
	toml := "TripleStoreURL = \"https://box\"\nNamespace = \"someones-namespace\"\nCollection = \"someones-collection\"\n"
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	c.Namespace = "atlas"
	c.Collection = "measurements"

	got := c.ResolveCollection("")
	if got.Namespace != "atlas" || got.Collection != "measurements" {
		t.Errorf("override ignored: resolved %q/%q", got.Namespace, got.Collection)
	}
	if got.TripleStoreURL != "https://box" {
		t.Errorf("override must not disturb the connection: %+v", got)
	}
}
