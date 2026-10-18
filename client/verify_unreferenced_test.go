package client

// Tests for #124: a directory node whose structural marker is gone *and* which no
// edge points at. #122's detection is edge-driven — the node turns up because a
// child's parent edge names it — so a childless, edge-less node with a `path` and
// no `tie-type` is in neither universe and is reported by nothing. It stays
// browsable (a parent lists children by the reverse parent query), which is what
// makes it easy to miss: it simply escapes the orphan, metadata and cycle checks.

import (
	"testing"

	"github.com/uidbz/tie/api"
)

func TestVerifyUnreferencedDirectoryWithoutTieType(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/keep/photos/deep")
	writeFile(t, dir+"/keep/photos/a.jpg", "one")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	photos, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep/photos")
	if err != nil || photos == "" {
		t.Fatalf("DirUIDFromPath(photos) = %q, %v", photos, err)
	}
	deep, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep/photos/deep")
	if err != nil || deep == "" {
		t.Fatalf("DirUIDFromPath(deep) = %q, %v", deep, err)
	}

	// Take away everything verify could follow: the structural marker, and the
	// one edge that mentions the node at all. What is left is a `path` triple.
	row, err := tie.Get(string(deep))
	if err != nil {
		t.Fatalf("Get(%s): %v", short(string(deep)), err)
	}
	for _, v := range RowValues(row, RelType) {
		if _, err := tie.Delete(string(deep), string(RelType), v); err != nil {
			t.Fatalf("dropping tie-type %q: %v", v, err)
		}
	}
	for _, v := range RowValues(row, RelParent) {
		if _, err := tie.Delete(string(deep), string(RelParent), v); err != nil {
			t.Fatalf("dropping parent %q: %v", v, err)
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := tie.Verify(false); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if findingOf(rep, "missing-tie-type", string(deep)) == nil {
		t.Errorf("a directory carrying a path and no marker, referenced by nothing, should still be enumerated; findings=%v", rep.Findings())
	}

	// The repair side already existed: the marker is re-derived from the path,
	// and the node — now a visible orphan — goes back under the directory its
	// path names.
	res, err := tie.RepairTree(rep, RepairOptions{Apply: true})
	if err != nil {
		t.Fatalf("RepairTree --fix: %v", err)
	}
	if !planHasOp(res.Planned, api.BatchAdd, string(deep), string(RelType)) {
		t.Errorf("repair should re-derive the marker; planned=%v", res.Planned)
	}
	if _, err := tie.Verify(false); err != nil {
		t.Fatalf("Verify after repair: %v", err)
	}
	back, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep/photos/deep")
	if err != nil || back != deep {
		t.Errorf("DirUIDFromPath(deep) after repair = %q, %v; want %s", back, err, deep)
	}
	photodir, err := ReadTieDir(tie, photos)
	if err != nil {
		t.Fatalf("ReadTieDir(photos): %v", err)
	}
	var listed bool
	for _, sd := range photodir.SubDirs {
		if sd.Uid == deep {
			listed = true
		}
	}
	if !listed {
		t.Errorf("%s should list %s again after the repair; subdirs=%v", short(string(photos)), short(string(deep)), photodir.SubDirs)
	}
}
