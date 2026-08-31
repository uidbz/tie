package client

import (
	"reflect"
	"testing"
)

func TestResolveCollectionFallbacks(t *testing.T) {
	c := Config{
		Username:         "top",
		Password:         "toppw",
		Namespace:        "TopNS",
		Collection:       "topcoll",
		DaemonURL:        "https://main",
		DefaultFileHosts: []string{"media"},
		Collections: map[string]CollectionEntry{
			"images": {Namespace: "Pics", Collection: "images", FileHosts: []string{"ssd"}},
			"archive": {
				DaemonURL: "https://archive-box",
				Namespace: "Cold",
				Username:  "arch",
				Password:  "archpw",
			},
		},
	}

	got := c.ResolveCollection("images")
	want := ResolvedCollection{
		Namespace:  "Pics",
		Collection: "images",
		DaemonURL:  "https://main", // inherited from top-level
		Username:   "top",          // inherited
		Password:   "toppw",
		FileHosts:  []string{"ssd"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("images:\n got %+v\nwant %+v", got, want)
	}

	got = c.ResolveCollection("archive")
	want = ResolvedCollection{
		Namespace:  "Cold",
		Collection: "archive", // falls back to the entry name
		DaemonURL:  "https://archive-box",
		Username:   "arch", // entry creds used as a pair
		Password:   "archpw",
		FileHosts:  []string{"media"}, // inherited DefaultFileHosts
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("archive:\n got %+v\nwant %+v", got, want)
	}
}

func TestResolveCollectionUnknownNameIsBareID(t *testing.T) {
	c := Config{Namespace: "NS", Collection: "def", DaemonURL: "https://d"}
	got := c.ResolveCollection("adhoc")
	if got.Collection != "adhoc" || got.Namespace != "NS" || got.DaemonURL != "https://d" {
		t.Errorf("unknown name should be a bare collection id on the top-level daemon: %+v", got)
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

	if c.DaemonURL != "https://legacy" {
		t.Errorf("Webservice should populate DaemonURL, got %q", c.DaemonURL)
	}
	if c.DefaultCollection != "Main" {
		t.Errorf("DefaultCollection should be synthesized as %q, got %q", "Main", c.DefaultCollection)
	}
	entry, ok := c.Collections["Main"]
	if !ok {
		t.Fatalf("expected a synthesized 'Main' collection, got %+v", c.Collections)
	}
	if entry.Namespace != "Collections" || !reflect.DeepEqual(entry.FileHosts, []string{"default"}) {
		t.Errorf("synthesized entry wrong: %+v", entry)
	}

	// The synthesized default must resolve to the legacy connection.
	got := c.ResolveCollection("")
	if got.DaemonURL != "https://legacy" || got.Collection != "Main" || got.Namespace != "Collections" {
		t.Errorf("resolved default from legacy config wrong: %+v", got)
	}
}
