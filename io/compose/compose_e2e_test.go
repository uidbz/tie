package compose

// End-to-end Build against the in-memory fake filehost: the pure selectEntries
// semantics are unit-tested in select_test;
// this pins the whole path — fetch real manifests, merge several specs with
// As-renames and a whole-tree splice, upload the synthesized manifests, and
// get back a root hash that actually resolves on the filehost and walks to
// the selected entries. That round-trip is what a job's InputHash depends on.

import (
	"strings"
	"testing"
)

func TestComposeEndToEndMultiSpecMerge(t *testing.T) {
	aLeaf1, aLeaf2, aDeep, bLeaf := hashOf("a1"), hashOf("a2"), hashOf("adeep"), hashOf("b1")
	subHash, aHash, bHash := hashOf("asub"), hashOf("atree"), hashOf("btree")
	blobs := map[string]string{
		aHash:   dirManifest(fileEntry("a1.csv", aLeaf1), fileEntry("a2.tsv", aLeaf2), dirEntry("sub", subHash)),
		subHash: dirManifest(fileEntry("deep.txt", aDeep)),
		aLeaf1:  "x1", aLeaf2: "x2", aDeep: "x3",
		bHash: dirManifest(fileEntry("b1.csv", bLeaf)),
		bLeaf: "y1",
	}
	f, url := newFakeFilehost(t, blobs)

	hash, err := Build(url, "store", []Spec{
		{Hash: aHash, Paths: []string{"a1.csv"}, As: "first.csv"}, // File selection + rename
		{Hash: aHash, Glob: "*.tsv"},                              // glob from the same tree
		{Hash: bHash},                                             // whole-tree splice
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if hash == "" {
		t.Fatal("Compose returned an empty root hash")
	}

	// The returned hash must resolve on the filehost — that is the whole
	// contract of the value handed to a job as InputHash.
	items, err := DirTree(url, hash, "")
	if err != nil {
		t.Fatalf("composed root %s does not resolve on the filehost: %v", shortHash(hash), err)
	}
	if len(items) != 1 || items[0].MediaType != "dir" {
		t.Fatalf("composed root is not a single directory: %+v", items)
	}

	got := map[string]string{}
	for _, c := range items[0].Children {
		got[c.Name] = c.Hash
	}
	want := map[string]string{"first.csv": aLeaf1, "a2.tsv": aLeaf2, "b1.csv": bLeaf}
	if len(got) != len(want) {
		t.Fatalf("composed children = %v, want %v", got, want)
	}
	for name, h := range want {
		if got[name] != h {
			t.Errorf("composed %q has hash %s, want the source entry's %s", name, got[name], h)
		}
	}
	// The rename renamed only the manifest entry: the content address is the
	// source leaf's, so no bytes moved.
	if got["first.csv"] != aLeaf1 {
		t.Errorf("As rename lost the content address: %s, want %s", got["first.csv"], aLeaf1)
	}
	// The composed manifest itself was uploaded (PUT), not just computed:
	// DirTree above proves it is retrievable, and the two source trees were
	// each fetched exactly once.
	if f.hitCount(aHash) != 1 {
		t.Errorf("source tree A fetched %d times, want 1 (deduped across specs)", f.hitCount(aHash))
	}
}

// A collision between two specs must surface as Compose's error (naming
// .As()), not as a silently lost file in the composed tree.
func TestComposeCollisionNamesBothSources(t *testing.T) {
	// Labels chosen so hashOf maps them to DISTINCT fake addresses (hashOf
	// folds non-hex letters to 'a', so "xtree"/"ytree" would collide).
	xLeaf, yLeaf := hashOf("leaf1"), hashOf("leaf2")
	xHash, yHash := hashOf("tree1"), hashOf("tree2")
	blobs := map[string]string{
		xHash: dirManifest(fileEntry("data.csv", xLeaf)), xLeaf: "x",
		yHash: dirManifest(fileEntry("data.csv", yLeaf)), yLeaf: "y",
	}
	_, url := newFakeFilehost(t, blobs)

	_, err := Build(url, "store", []Spec{{Hash: xHash}, {Hash: yHash}})
	if err == nil {
		t.Fatal("two different files named data.csv must collide, got nil error")
	}
	if !strings.Contains(err.Error(), "collision") || !strings.Contains(err.Error(), ".As()") {
		t.Errorf("collision error %q should name the collision and suggest .As()", err)
	}
}
