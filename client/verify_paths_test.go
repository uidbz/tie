package client

// Tests for #118 and #119: what Verify must say about a directory node whose
// path triple is gone, and about one whose path contradicts the parent that
// lists it. Both states make a directory invisible to every path-based view
// (tie-view's tree, tie-fm, the FUSE mount), which is how they get reported as
// "missing directories".

import (
	"strings"
	"testing"

	"github.com/uidbz/tie/api"
)

// findingOf returns the first finding of kind recorded against subject.
func findingOf(rep *VerifyReport, kind, subject string) *Finding {
	fs := rep.Findings()
	for i := range fs {
		if fs[i].Kind == kind && fs[i].Subject == subject {
			return &fs[i]
		}
	}
	return nil
}

func planHasOp(ops []RepairOp, kind, key, relation string) bool {
	for _, op := range ops {
		if op.Kind == kind && op.Key == key && op.Relation == relation {
			return true
		}
	}
	return false
}

// TestVerifyPathlessDirNotMissingBlob covers #118. A directory node that lost
// its path triple used to be dropped into the file universe ("tiedir blob: a
// directory's content, not a node"), where --check-blobs HEADs its key on the
// filehost: a DirUID is never content, so the only answer is "no such blob", and
// verify reports a false missing-blob for a real directory.
func TestVerifyPathlessDirNotMissingBlob(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "hello")
	mkdir(t, dir+"/photos")
	writeFile(t, dir+"/photos/b.jpg", "world")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	uid, err := tie.DirUIDFromPath(FileURIScheme + dir + "/photos")
	if err != nil || uid == "" {
		t.Fatalf("DirUIDFromPath(photos) = %q, %v", uid, err)
	}
	row, err := tie.Get(string(uid))
	if err != nil {
		t.Fatalf("Get(%s): %v", short(string(uid)), err)
	}
	for _, p := range RowValues(row, RelPath) {
		if _, err := tie.Delete(string(uid), string(RelPath), p); err != nil {
			t.Fatalf("deleting path %q: %v", p, err)
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rep, err := tie.Verify(true) // --check-blobs
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, h := range rep.MissingBlobs {
		if h == string(uid) {
			t.Errorf("verify reported the DirUID %s as a missing blob: a directory key is never content", short(h))
		}
	}
	f := findingOf(rep, "pathless-dir", string(uid))
	if f == nil {
		t.Errorf("verify must report the path-less directory; findings=%v", rep.Findings())
	} else if !strings.Contains(f.Value1+f.Value2, "photos") {
		t.Errorf("the pathless-dir finding should name the directory and the path it should have, got %+v", f)
	}
	for _, o := range rep.OrphanDirs {
		if o == uid {
			t.Errorf("%s still has a parent edge; it is not an orphan", short(string(uid)))
		}
	}
}

// TestVerifyDirectoryWithoutTieType covers #122. A node with a `path` triple is
// a directory — content nodes carry no path — but verify's tree universe comes
// from the reverse tie-type query, so a directory whose structural tie-type
// triple was destroyed is in neither universe. Its children then read as
// dangling parent references, and a fixing repair re-parents them to the
// grandparent: a folder's contents moved a level up, away from the folder that
// is sitting right there.
func TestVerifyDirectoryWithoutTieType(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/keep/photos")
	writeFile(t, dir+"/keep/photos/a.jpg", "one")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	keep, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep")
	if err != nil || keep == "" {
		t.Fatalf("DirUIDFromPath(keep) = %q, %v", keep, err)
	}
	photos, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep/photos")
	if err != nil || photos == "" {
		t.Fatalf("DirUIDFromPath(photos) = %q, %v", photos, err)
	}
	row, err := tie.Get(string(photos))
	if err != nil {
		t.Fatalf("Get(%s): %v", short(string(photos)), err)
	}
	for _, v := range RowValues(row, RelType) {
		if _, err := tie.Delete(string(photos), string(RelType), v); err != nil {
			t.Fatalf("dropping tie-type %q: %v", v, err)
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	a := hashOf(t, dir+"/keep/photos/a.jpg")
	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if f := findingOf(rep, "dangling-parent", a); f != nil {
		t.Errorf("%s's parent carries a path, so it is a directory; findings=%v", short(a), rep.Findings())
	}
	if findingOf(rep, "missing-tie-type", string(photos)) == nil {
		t.Errorf("verify should report the directory whose structural marker is gone; findings=%v", rep.Findings())
	}

	res, err := tie.RepairTree(rep, RepairOptions{Apply: true})
	if err != nil {
		t.Fatalf("RepairTree --fix: %v", err)
	}
	if !planHasOp(res.Planned, api.BatchAdd, string(photos), string(RelType)) {
		t.Errorf("repair should plan to re-derive the tie-type of %s; planned=%v", short(string(photos)), res.Planned)
	}
	after, err := tie.Get(string(photos))
	if err != nil || !RowHas(after, RelType, str(TieDirectory)) {
		t.Errorf("%s is not marked a directory after repair: %+v err=%v", short(string(photos)), after, err)
	}
	crow, err := tie.Get(a)
	if err != nil {
		t.Fatalf("Get(%s): %v", short(a), err)
	}
	if !RowHas(crow, RelParent, string(photos)) {
		t.Errorf("%s should still be a child of %s, parents=%v", short(a), short(string(photos)), RowValues(crow, RelParent))
	}
	rep2, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify after repair: %v", err)
	}
	if rep2.Problems() != 0 {
		t.Errorf("problems left after repair: %+v", rep2)
	}
	_ = keep
}

// TestRepairKeepsPathlessDirWithManifest is the destructive half of #118. The
// node above was saved from the ghost test by triples it happens to carry (a
// name, a size); strip those and nothing but the structural marker, the manifest
// link and the parent edge is left — the shape of a real directory whose
// metadata was half-dropped. Repair used to delete every triple it had left and
// move its children up a level, silently, because the node was unreachable
// before and after.
func TestRepairKeepsPathlessDirWithManifest(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/photos")
	writeFile(t, dir+"/photos/b.jpg", "world")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	uid, err := tie.DirUIDFromPath(FileURIScheme + dir + "/photos")
	if err != nil || uid == "" {
		t.Fatalf("DirUIDFromPath(photos) = %q, %v", uid, err)
	}
	row, err := tie.Get(string(uid))
	if err != nil {
		t.Fatalf("Get(%s): %v", short(string(uid)), err)
	}
	hash := hashOf(t, dir+"/photos/b.jpg")
	drop := []Relation{RelPath, RelFilename, RelName, RelFilesize, RelMediaType}
	for _, rel := range drop {
		for _, v := range RowValues(row, rel) {
			if _, err := tie.Delete(string(uid), string(rel), v); err != nil {
				t.Fatalf("dropping %s: %v", rel, err)
			}
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	res, err := tie.RepairTree(rep, RepairOptions{Apply: true})
	if err != nil {
		t.Fatalf("RepairTree --fix: %v", err)
	}
	// The directory keeps its manifest link: it is a folder, not leftover.
	after, err := tie.Get(string(uid))
	if err != nil || len(RowValues(after, RelTiedirHash)) == 0 {
		t.Errorf("repair deleted the triples of a real directory (%s): %+v err=%v",
			short(string(uid)), res.Planned, err)
	}
	// Its child still hangs off it, not off the grandparent.
	crow, err := tie.Get(hash)
	if err != nil {
		t.Fatalf("Get(%s): %v", short(hash), err)
	}
	if !RowHas(crow, RelParent, string(uid)) {
		t.Errorf("%s should still be a child of %s, parents=%v",
			short(hash), short(string(uid)), RowValues(crow, RelParent))
	}
}

// TestVerifyPathMismatch covers #119: the directory lives under a parent that is
// nowhere near the location its path triple claims, so the branch its path names
// does not list it. Verify checked duplicate path claims but never this.
func TestVerifyPathMismatch(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/photos")
	writeFile(t, dir+"/photos/a.txt", "hello")
	mkdir(t, dir+"/elsewhere")
	writeFile(t, dir+"/elsewhere/b.txt", "there")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	uid, err := tie.DirUIDFromPath(FileURIScheme + dir + "/photos")
	if err != nil || uid == "" {
		t.Fatalf("DirUIDFromPath(photos) = %q, %v", uid, err)
	}
	elsewhere, err := tie.DirUIDFromPath(FileURIScheme + dir + "/elsewhere")
	if err != nil || elsewhere == "" {
		t.Fatalf("DirUIDFromPath(elsewhere) = %q, %v", elsewhere, err)
	}
	root, err := tie.DirUIDFromPath(FileURIScheme + dir)
	if err != nil || root == "" {
		t.Fatalf("DirUIDFromPath(import root) = %q, %v", root, err)
	}
	row, err := tie.Get(string(uid))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Move the node: drop the real parent edge, add one to an unrelated dir.
	for _, p := range RowValues(row, RelParent) {
		if _, err := tie.Delete(string(uid), string(RelParent), p); err != nil {
			t.Fatalf("deleting parent edge: %v", err)
		}
	}
	if _, err := tie.Add(string(uid), string(RelParent), string(elsewhere)); err != nil {
		t.Fatalf("adding parent edge: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	f := findingOf(rep, "path-mismatch", string(uid))
	if f == nil {
		t.Errorf("verify must report the path/parent disagreement; findings=%v", rep.Findings())
		return
	}
	if !strings.Contains(f.Value1, "/photos") {
		t.Errorf("finding should carry the claimed path, got %q", f.Value1)
	}
	if !strings.Contains(f.Value2, "/elsewhere") {
		t.Errorf("finding should carry where the node actually is, got %q", f.Value2)
	}

	// The repair direction the operator wants: adopt the path, i.e. put the
	// node back under the directory its path names (here the import root).
	res, err := tie.RepairTree(rep, RepairOptions{})
	if err != nil {
		t.Fatalf("RepairTree: %v", err)
	}
	if !planHasOp(res.Planned, api.BatchAdd, string(uid), string(RelParent)) {
		t.Errorf("repair should plan to re-parent %s under the directory its path names; planned=%v",
			short(string(uid)), res.Planned)
	}
	_ = root
}
