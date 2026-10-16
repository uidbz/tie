package client

import (
	"os"
	"testing"
)

// scopeConfig has three collections on one triplestore: the bound "main", a
// plain "other", and "music" whose config entry names a different collection
// id ("Music2") — the case the per-call collection parameter got wrong, because
// it used the name verbatim as a collection id.
func scopeConfig() Config {
	c := TestingConfig()
	c.Namespace = "scoptesting"
	c.Collection = "main"
	c.DefaultFileHosts = []string{"default"}
	c.FileHosts = map[string]FileHost{"default": {URL: testingFileHostURL}}
	c.Collections = map[string]CollectionEntry{
		"other": {Collection: "other"},
		"music": {Collection: "Music2"},
	}
	return c
}

func scopeClient(t *testing.T, drop ...string) *TieClient {
	t.Helper()
	tc := NewTieClient(scopeConfig())
	for _, name := range drop {
		if err := tc.In(name).DropCollection(); err != nil {
			t.Fatalf("dropping %q: %v", name, err)
		}
	}
	return tc
}

// TestInScopesEveryCall is the §7.2 guard: a scoped client answers from its own
// collection, and the transport is shared when the connection is the same.
func TestInScopesEveryCall(t *testing.T) {
	tc := scopeClient(t, "main", "other", "music")

	if got := tc.In(""); got != tc {
		t.Error("In(\"\") returned a different client")
	}
	if got := tc.In("main"); got != tc {
		t.Error("In() of the bound collection returned a different client")
	}

	other := tc.In("other")
	if other == tc {
		t.Fatal("In(\"other\") returned the bound client")
	}
	if other.client != tc.client {
		t.Error("In() built a new transport for the same triplestore and credentials")
	}
	if id := other.CollectionInfo().CollectionId; id != "other" {
		t.Errorf("In(\"other\") addresses collection %q", id)
	}
	// The config entry's collection id, not the entry's name.
	if id := tc.In("music").CollectionInfo().CollectionId; id != "Music2" {
		t.Errorf("In(\"music\") addresses collection %q, want Music2", id)
	}

	// A node created in one scope is invisible from the other.
	if _, err := other.MkTieDirAll("tie:/scoped"); err != nil {
		t.Fatal(err)
	}
	uid, err := other.DirUIDFromPath("tie:/scoped")
	if err != nil || uid == "" {
		t.Errorf("the scoped client cannot find what it created: uid=%q err=%v", uid, err)
	}
	uid, err = tc.DirUIDFromPath("tie:/scoped")
	if err != nil && uid != "" {
		t.Errorf("the node leaked into the bound collection: uid=%q err=%v", uid, err)
	}
}

// TestImportDirStaysInItsCollection is the §3.8 bug: ImportDir wrote its tag
// batches to the collection argument, but MkTieDirAll, DirUIDFromPath and
// reconcileDir used the client's bound collection. `tie import --collection X`
// therefore created the directory nodes in the default collection and the file
// triples in X, with parent edges pointing at DirUIDs that do not exist in X —
// a tree that verify reports as dangling parent references.
func TestImportDirStaysInItsCollection(t *testing.T) {
	tc := scopeClient(t, "main", "other", "music")

	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/sub/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The CLI's shape: one config, a client scoped to the named collection.
	if err := tc.In("other").ImportDir(dir, tc.Config.FileHosts["default"], "directory", nil, "/imported", 0); err != nil {
		t.Fatal(err)
	}

	other := tc.In("other")
	dirs, err := other.allOfType(str(TieDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Error("no directory nodes in the imported collection: they went to the bound one")
	}
	files, err := other.allOfType(str(TieFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Error("no file rows in the imported collection")
	}

	// The bound collection must be untouched: not one node.
	stray, err := tc.allOfType(str(TieDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if len(stray) != 0 {
		t.Errorf("%d directory nodes were written to the bound collection", len(stray))
	}
	if strayFiles, err := tc.allOfType(str(TieFile)); err == nil && len(strayFiles) != 0 {
		t.Errorf("%d file rows were written to the bound collection", len(strayFiles))
	}
}

// TestImportDirResolvesTheConfigEntryName: `--collection music` means the
// [Collections.music] entry, which addresses the collection "Music2". The
// parameter used to send the literal name, writing to a collection called
// "music" while taking its filehosts from the entry.
func TestImportDirResolvesTheConfigEntryName(t *testing.T) {
	tc := scopeClient(t, "main", "other", "music")

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/b.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tc.In("music").ImportDir(dir, tc.Config.FileHosts["default"], "directory", nil, "/records", 0); err != nil {
		t.Fatal(err)
	}
	music := tc.In("music")

	files, err := music.allOfType(str(TieFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("the entry's collection holds %d files, want 1", len(files))
	}

	// Nothing was written under the bare name.
	bare := NewTieClient(scopeConfig())
	bare.Config.Collections = nil
	bare.Config.Collection = "music"
	if rows, err := bare.allOfType(str(TieFile)); err == nil && len(rows) != 0 {
		t.Errorf("a collection literally named \"music\" holds %d rows", len(rows))
	}
}
