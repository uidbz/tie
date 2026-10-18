package client

// Tests for #120: what a re-home must preserve. A directory that still carries
// its path triple knows where it belonged, and a directory's tiedir manifest
// knows what it contained. Re-homing flattened both, which is how a collection
// ends up with a recovery directory holding thousands of unrelated siblings and
// path triples that point at a branch that no longer lists them.

import (
	"errors"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/api"
)

// TestRepairOrphansRestoresOriginalPlace: an orphan directory goes back under
// the parent its own path names, not into one flat recovery dir.
func TestRepairOrphansRestoresOriginalPlace(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/keep/photos")
	writeFile(t, dir+"/keep/photos/a.jpg", "one")
	writeFile(t, dir+"/keep/top.txt", "two")
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
	// Cut the directory loose, and a file with it.
	if _, err := tie.Delete(string(photos), string(RelParent), string(keep)); err != nil {
		t.Fatalf("cutting photos loose: %v", err)
	}
	top := hashOf(t, dir+"/keep/top.txt")
	if _, err := tie.Delete(top, string(RelParent), string(keep)); err != nil {
		t.Fatalf("cutting top.txt loose: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(rep.OrphanDirs) != 1 || rep.OrphanDirs[0] != photos {
		t.Fatalf("OrphanDirs = %v, want [%s]", rep.OrphanDirs, short(string(photos)))
	}
	if _, err := tie.RepairOrphans(rep, ""); err != nil {
		t.Fatalf("RepairOrphans: %v", err)
	}

	if got := singleParent(t, tie, string(photos)); got != string(keep) {
		t.Errorf("photos re-homed under %s, want its original parent %s", short(got), short(string(keep)))
	}
	// A file has no path of its own, so it still lands in the recovery dir.
	dest, err := tie.DirUIDFromPath(FileURIScheme + "/restored/" + time.Now().Format("2006-01-02"))
	if err != nil || dest == "" {
		t.Fatalf("recovery dir was not created: %q %v", dest, err)
	}
	if got := singleParent(t, tie, top); got != string(dest) {
		t.Errorf("top.txt re-homed under %s, want the recovery dir %s", short(got), short(string(dest)))
	}
	rep2, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify after repair: %v", err)
	}
	if rep2.Problems() != 0 {
		t.Errorf("tree still has problems after the re-home: %+v", rep2)
	}
}

// TestRepairOrphansCreatesMissingChain covers the harder case: the directory the
// orphan belonged to is gone as well, so the chain has to be created rather than
// found — otherwise the only place left to put the node is a flat recovery dir
// whose listing contradicts the path the node still carries.
func TestRepairOrphansCreatesMissingChain(t *testing.T) {
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
	// photos loses its parent edge; keep loses its path, so the location
	// photos' own path names no longer resolves to anything.
	if _, err := tie.Delete(string(photos), string(RelParent), string(keep)); err != nil {
		t.Fatalf("cutting photos loose: %v", err)
	}
	krow, err := tie.Get(string(keep))
	if err != nil {
		t.Fatalf("Get(keep): %v", err)
	}
	for _, p := range RowValues(krow, RelPath) {
		if _, err := tie.Delete(string(keep), string(RelPath), p); err != nil {
			t.Fatalf("dropping keep's path: %v", err)
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if _, err := tie.RepairOrphans(rep, ""); err != nil {
		t.Fatalf("RepairOrphans: %v", err)
	}

	want := FileURIScheme + dir + "/keep"
	rebuilt, err := tie.DirUIDFromPath(want)
	if err != nil || rebuilt == "" {
		t.Fatalf("no directory at %s after the re-home: %v", want, err)
	}
	if got := singleParent(t, tie, string(photos)); got != string(rebuilt) {
		t.Errorf("photos re-homed under %s, want %s (the directory its path names)", short(got), want)
	}
	// The rebuilt level is itself reachable from the root.
	if got := singleParent(t, tie, string(rebuilt)); got == "" {
		t.Errorf("%s has no parent: the chain was created but left unreachable", want)
	}
	if base := path.Base(want); base != "keep" {
		t.Fatalf("test expects a named parent, got %q", want)
	}
}

// TestRelinkFromManifestsRestoresContainment is the second half of #120. After a
// bulk re-home the files sit flat in one recovery directory while their folder
// is back where it belongs; the folder's tiedir manifest still records what it
// contained, so the containment is rebuildable — a file entry by content hash, a
// subdirectory entry by the manifest it is the snapshot of.
func TestRelinkFromManifestsRestoresContainment(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/keep/photos")
	writeFile(t, dir+"/keep/photos/a.jpg", "one")
	writeFile(t, dir+"/keep/photos/b.jpg", "two")
	writeFile(t, dir+"/keep/x.txt", "three")
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

	// Scatter every node, then re-home: directories go back to their own paths,
	// files land flat in the recovery dir — the shape a bulk re-home leaves.
	loose := []string{string(keep), string(photos),
		hashOf(t, dir+"/keep/photos/a.jpg"), hashOf(t, dir+"/keep/photos/b.jpg"),
		hashOf(t, dir+"/keep/x.txt")}
	for _, k := range loose {
		row, err := tie.Get(k)
		if err != nil {
			t.Fatalf("Get(%s): %v", short(k), err)
		}
		for _, p := range RowValues(row, RelParent) {
			if _, err := tie.Delete(k, string(RelParent), p); err != nil {
				t.Fatalf("loosening %s: %v", short(k), err)
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
	if len(rep.OrphanFiles) != 3 || len(rep.OrphanDirs) != 2 {
		t.Fatalf("scattering left %d orphan files and %d orphan dirs, want 3 and 2",
			len(rep.OrphanFiles), len(rep.OrphanDirs))
	}
	if _, err := tie.RepairOrphans(rep, ""); err != nil {
		t.Fatalf("RepairOrphans: %v", err)
	}

	a, b, x := loose[2], loose[3], loose[4]
	if p := singleParent(t, tie, a); !isRecoveryHome(t, tie, p) {
		t.Fatalf("a.jpg should sit in the recovery dir after the re-home, got %s", short(p))
	}
	// One entry was deleted after the import. Its directory's manifest still
	// lists it, and relink must not answer that by creating a node out of a
	// single parent triple.
	brow, err := tie.Get(b)
	if err != nil {
		t.Fatalf("Get(b.jpg): %v", err)
	}
	for rel, vals := range brow.Attributes {
		for _, v := range vals {
			if _, err := tie.Delete(b, rel, v); err != nil {
				t.Fatalf("deleting b.jpg's %s: %v", rel, err)
			}
		}
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Plan first: nothing changes, and the plan names the recorded parent.
	plan, err := tie.RelinkFromManifests(RelinkOptions{})
	if err != nil {
		t.Fatalf("RelinkFromManifests (plan): %v", err)
	}
	if !planHasOp(plan.Planned, api.BatchAdd, a, string(RelParent)) {
		t.Errorf("plan should re-parent %s under %s; planned=%d ops", short(a), short(string(photos)), len(plan.Planned))
	}
	if got := singleParent(t, tie, a); !isRecoveryHome(t, tie, got) {
		t.Errorf("planning changed the tree: %s now sits under %s", short(a), short(got))
	}

	res, err := tie.RelinkFromManifests(RelinkOptions{Apply: true, DropStale: true})
	if err != nil {
		t.Fatalf("RelinkFromManifests --apply: %v", err)
	}
	if res.Applied != len(res.Planned) {
		t.Errorf("applied %d of %d planned", res.Applied, len(res.Planned))
	}
	for hash, want := range map[string]DirUID{a: photos, x: keep} {
		if got := singleParent(t, tie, hash); got != string(want) {
			t.Errorf("%s is under %s, want only %s (its manifest's directory)",
				short(hash), short(got), short(string(want)))
		}
	}
	if _, err := tie.Get(b); !errors.Is(err, ErrNotFound) {
		t.Errorf("relink resurrected the deleted %s (Get err = %v)", short(b), err)
	}
	rep2, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify after relink: %v", err)
	}
	if rep2.Problems() != 0 {
		t.Errorf("problems left after relink: %+v", rep2)
	}
}

// TestRelinkResolvesSubdirectoryByIdentity: a manifest lists a subdirectory by
// the hash of *its* manifest, not by its DirUID, so the entry has to be resolved
// through the tiedir-hash link. Matching on the name instead would re-parent
// whatever folder happens to be called that.
func TestRelinkResolvesSubdirectoryByIdentity(t *testing.T) {
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
	recovery, err := tie.MkTieDirAll(FileURIScheme + "/restored/2000-01-01")
	if err != nil {
		t.Fatalf("MkTieDirAll: %v", err)
	}
	// Re-home photos flat: its own path still claims it belongs to keep.
	if _, err := tie.Delete(string(photos), string(RelParent), string(keep)); err != nil {
		t.Fatalf("cutting photos loose: %v", err)
	}
	if _, err := tie.Add(string(photos), string(RelParent), string(recovery)); err != nil {
		t.Fatalf("re-homing photos: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	res, err := tie.RelinkFromManifests(RelinkOptions{Apply: true, DropStale: true})
	if err != nil {
		t.Fatalf("RelinkFromManifests --apply: %v", err)
	}
	if got := singleParent(t, tie, string(photos)); got != string(keep) {
		t.Errorf("photos is under %s, want %s; ops=%d", short(got), short(string(keep)), res.Applied)
	}
	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.Problems() != 0 {
		t.Errorf("problems left after relink: %+v", rep)
	}
}

// isRecoveryHome reports whether uid is a directory under tie:/restored/.
func isRecoveryHome(t *testing.T, tie *TieClient, uid string) bool {
	t.Helper()
	row, err := tie.Get(uid)
	if err != nil {
		return false
	}
	for _, p := range RowValues(row, RelPath) {
		if strings.HasPrefix(p, FileURIScheme+"/restored/") {
			return true
		}
	}
	return false
}
