package client

import (
	"os"
	"path/filepath"
	"testing"
)

// importFixtureTree writes root/loose.flac and root/CD1/track.flac and returns
// the root.
func importFixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"loose.flac", "CD1/track.flac"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("bytes of "+rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func rowOf(t *testing.T, tc *TieClient, key string) Row {
	t.Helper()
	row, err := tc.Get(key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return row
}

func valuesOf(row Row, relation string) []string {
	return row.Attributes[relation]
}

// TestImportDirNamesEveryNode pins what the import engine owes a tree: every
// directory node exists below the import root, carries its own name and a link
// to its snapshot blob, and every file sits under the parent it came from.
func TestImportDirNamesEveryNode(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	dir := importFixtureTree(t)
	if err := tc.ImportDir(dir, tc.Config.FileHosts["default"], "audio-dir", []string{"imported"}, "/trees", 0); err != nil {
		t.Fatal(err)
	}

	for _, vpath := range []string{"tie:/trees", "tie:/trees/CD1"} {
		uid, err := tc.DirUIDFromPath(vpath)
		if err != nil || uid == "" {
			t.Fatalf("%s has no directory node (uid=%q, err=%v)", vpath, uid, err)
		}
		row := rowOf(t, tc, string(uid))
		name := filepath.Base(vpath)
		if !RowHas(row, str(TieFilename), name) {
			t.Errorf("%s node is not named %q: %v", vpath, name, row.Attributes[str(TieFilename)])
		}
		if len(valuesOf(row, str(TieTiedirHash))) == 0 {
			t.Errorf("%s node has no tiedir snapshot link", vpath)
		}
		if vpath == "tie:/trees" && !RowHas(row, str(TieTypeProperty), "audio-dir") {
			t.Errorf("%s root is not stamped audio-dir: %v", vpath, valuesOf(row, str(TieTypeProperty)))
		}
	}

	// The file below CD1 has CD1 as its parent, not the root.
	rootUID, _ := tc.DirUIDFromPath("tie:/trees")
	subUID, _ := tc.DirUIDFromPath("tie:/trees/CD1")
	files, err := tc.allOfType(str(TieFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("the import produced %d file rows, want 2", len(files))
	}
	byName := map[string]Row{}
	for _, f := range files {
		byName[f.Attributes[str(TieFilename)][0]] = f
	}
	if got := valuesOf(byName["track.flac"], str(TieParent)); len(got) != 1 || got[0] != string(subUID) {
		t.Errorf("track.flac parents = %v, want [%s]", got, subUID)
	}
	if got := valuesOf(byName["loose.flac"], str(TieParent)); len(got) != 1 || got[0] != string(rootUID) {
		t.Errorf("loose.flac parents = %v, want [%s]", got, rootUID)
	}
}

// TestImportDirReconcilesAndAlbumImportDoesNot: the plan says who owns a
// directory. A whole-tree import supersedes what the tree no longer holds; an
// album group is a subset view and must leave its neighbours alone.
func TestImportDirReconcilesAndAlbumImportDoesNot(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	dir := importFixtureTree(t)
	host := tc.Config.FileHosts["default"]

	// Album groups: two groups sharing one destination directory. Neither may
	// version away the other's file, because each is a subset view.
	g1 := AlbumGroup{
		SourceDir: dir,
		Files:     []string{filepath.Join(dir, "CD1", "track.flac")},
		SubPaths:  map[string]string{filepath.Join(dir, "CD1", "track.flac"): "cd1/track.flac"},
		Dest:      "/albums/shared",
	}
	g2 := AlbumGroup{
		SourceDir: dir,
		Files:     []string{filepath.Join(dir, "loose.flac")},
		SubPaths:  map[string]string{filepath.Join(dir, "loose.flac"): "loose.flac"},
		Dest:      "/albums/shared",
	}
	opts := AlbumImportOptions{Host: host, DirType: "audio-dir"}
	if err := tc.importAlbum(g1, opts); err != nil {
		t.Fatal(err)
	}
	if err := tc.importAlbum(g2, opts); err != nil {
		t.Fatal(err)
	}

	// The intermediate cd1 node the group's SubPaths created is named: an
	// anonymous node would disappear from listings.
	uid, err := tc.DirUIDFromPath("tie:/albums/shared/cd1")
	if err != nil || uid == "" {
		t.Fatalf("tie:/albums/shared/cd1 has no node (uid=%q, err=%v)", uid, err)
	}
	if row := rowOf(t, tc, string(uid)); !RowHas(row, str(TieFilename), "cd1") {
		t.Errorf("the cd1 node is not named cd1: %v", valuesOf(row, str(TieFilename)))
	}

	// Neither group versioned away the other's file: each placed one, and both
	// are still live where they sit.
	rootUID, _ := tc.DirUIDFromPath("tie:/albums/shared")
	live, err := fileChildren(tc, rootUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Filename != "loose.flac" {
		t.Errorf("the shared root has children %v, want [loose.flac]", namesOf(live))
	}
	live, err = fileChildren(tc, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Filename != "track.flac" {
		t.Errorf("the cd1 node has children %v, want [track.flac]", namesOf(live))
	}

	// A whole-tree import into the same destination does reconcile: drop one
	// file from the tree and re-import, and its content is versioned away.
	if err := os.Remove(filepath.Join(dir, "loose.flac")); err != nil {
		t.Fatal(err)
	}
	if err := tc.ImportDir(dir, host, "audio-dir", nil, "/albums/shared", 0); err != nil {
		t.Fatal(err)
	}
	live, err = fileChildren(tc, rootUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("after a tree import that no longer contains loose.flac, the root still has %v", namesOf(live))
	}
}

func namesOf(children []childEntry) []string {
	out := make([]string, 0, len(children))
	for _, c := range children {
		out = append(out, c.Filename)
	}
	return out
}
