package client

import (
	"os"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// verify_test.go exercises Verify and RepairOrphans against a real daemon
// (test-env/start.sh), using the isolated "testing" collection. Each test
// drops the collection first so it runs against a known-empty tree regardless
// of what other tests left behind. The daemon must be current (the tests build
// a path tree with ImportDir and depend on the MissingRelation-free reverse
// tie-type queries Verify uses).

// verifyTestConfig returns a Config bound to the test-env daemon/filehost
// (2161/2162) and the isolated "testing" collection, so these tests neither
// touch the operator's live Main collection nor require system services on
// 1161/1162. Set TIE_TEST_WEBSERVICE / TIE_TEST_FILEHOST to override.
func verifyTestConfig() Config {
	c := TestingConfig()
	c.Collection = "verifytesting"
	if ws := os.Getenv("TIE_TEST_WEBSERVICE"); ws != "" {
		c.Webservice = ws
	} else {
		c.Webservice = "http://localhost:2161"
	}
	fh := "http://localhost:2162"
	if h := os.Getenv("TIE_TEST_FILEHOST"); h != "" {
		fh = h
	}
	c.FileHosts = map[string]FileHost{"default": {URL: fh}}
	c.DefaultFileHosts = []string{"default"}
	return c
}

// freshVerifyClient returns a client bound to an empty "verifytesting"
// collection on the test-env daemon.
func freshVerifyClient(t *testing.T) *TieClient {
	t.Helper()
	tie := NewTieClient(verifyTestConfig())
	requireServer(t, tie)
	if err := tie.DropCollection(); err != nil {
		t.Fatalf("dropping testing collection: %v", err)
	}
	return tie
}

// TestVerifyCleanTree builds a small directory tree via ImportDir and expects
// Verify to report no problems: every node is parented and fully tagged.
func TestVerifyCleanTree(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "hello")
	mkdir(t, dir+"/sub")
	writeFile(t, dir+"/sub/b.txt", "world")

	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	rep, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.Problems() != 0 {
		t.Fatalf("clean tree reported problems: %+v", rep)
	}
	if rep.DirCount == 0 || rep.FileCount == 0 {
		t.Errorf("expected non-zero dir/file counts, got dirs=%d files=%d", rep.DirCount, rep.FileCount)
	}
}

// TestVerifyDetectsOrphanFile orphans a file by deleting its parent edge and
// expects Verify to report exactly that file, then RepairOrphans to re-home it
// so a follow-up Verify is clean.
func TestVerifyDetectsOrphanFile(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "orphan me")
	mkdir(t, dir+"/sub")
	writeFile(t, dir+"/sub/b.txt", "keep me")

	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	// Find the file's hash and its parent UID, then cut the edge.
	hash := hashOf(t, dir+"/a.txt")
	parent := singleParent(t, tie, hash)
	if _, err := tie.Delete(hash, str(TieParent), parent); err != nil {
		t.Fatalf("Delete parent edge: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rep, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(rep.OrphanFiles) != 1 || rep.OrphanFiles[0] != hash {
		t.Fatalf("OrphanFiles = %v, want [%s]", rep.OrphanFiles, hash)
	}

	n, err := tie.RepairOrphans("", rep, "")
	if err != nil {
		t.Fatalf("RepairOrphans: %v", err)
	}
	if n != 1 {
		t.Errorf("RepairOrphans restored %d nodes, want 1", n)
	}

	rep2, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify after repair: %v", err)
	}
	if rep2.Problems() != 0 {
		t.Fatalf("tree still has problems after repair: %+v", rep2)
	}
}

// TestVerifyDetectsOrphanDir orphans a whole directory (with a file inside) by
// cutting the directory's parent edge. The directory is reported; its file is
// NOT (it still has a parent — the orphaned directory). Repairing the directory
// makes the whole subtree reachable again.
func TestVerifyDetectsOrphanDir(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/sub")
	writeFile(t, dir+"/sub/b.txt", "nested")

	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	// Resolve the subdirectory's UID and cut its parent edge.
	subUID, err := tie.DirUIDFromPath(FileURIScheme + dir + "/sub")
	if err != nil || subUID == "" {
		t.Fatalf("DirUIDFromPath: uid=%q err=%v", subUID, err)
	}
	parent := singleParent(t, tie, string(subUID))
	if _, err := tie.Delete(string(subUID), str(TieParent), parent); err != nil {
		t.Fatalf("Delete parent edge: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rep, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(rep.OrphanDirs) != 1 || rep.OrphanDirs[0] != subUID {
		t.Fatalf("OrphanDirs = %v, want [%s]", rep.OrphanDirs, subUID)
	}
	if len(rep.OrphanFiles) != 0 {
		t.Errorf("OrphanFiles = %v, want empty (nested file keeps its parent)", rep.OrphanFiles)
	}

	if _, err := tie.RepairOrphans("", rep, ""); err != nil {
		t.Fatalf("RepairOrphans: %v", err)
	}
	rep2, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify after repair: %v", err)
	}
	if rep2.Problems() != 0 {
		t.Fatalf("tree still has problems after repair: %+v", rep2)
	}
}

// TestVerifyMissingMetadata writes a file with no filename/filesize/media-type
// triples (an incomplete import) and expects Verify to flag the gap.
func TestVerifyMissingMetadata(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "metadata")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	// Introduce a bare file node: tie-type file + a parent, but no filename,
	// filesize or media-type. This is what an interrupted import leaves behind.
	bare := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	parentUID, err := tie.DirUIDFromPath(FileURIScheme + dir)
	if err != nil || parentUID == "" {
		t.Fatalf("DirUIDFromPath: uid=%q err=%v", parentUID, err)
	}
	if _, err := tie.Add(bare, str(TieTypeProperty), str(TieFile)); err != nil {
		t.Fatalf("Add tie-type: %v", err)
	}
	if _, err := tie.Add(bare, str(TieParent), string(parentUID)); err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rep, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var found *MetadataGap
	for i := range rep.MissingMetadata {
		if rep.MissingMetadata[i].Subject == bare {
			found = &rep.MissingMetadata[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no metadata gap reported for %s; report=%+v", bare, rep)
	}
	for _, want := range []string{str(TieFilename), str(TieFilesize), str(TieMediaType)} {
		if !contains(found.Missing, want) {
			t.Errorf("gap %v missing expected relation %q", found.Missing, want)
		}
	}
}

// TestVerifyDetectsCycle builds a parent cycle between two directories and
// expects Verify to report it (and never to attempt a repair).
func TestVerifyDetectsCycle(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/one")
	mkdir(t, dir+"/two")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	one, _ := tie.DirUIDFromPath(FileURIScheme + dir + "/one")
	two, _ := tie.DirUIDFromPath(FileURIScheme + dir + "/two")
	if one == "" || two == "" {
		t.Fatalf("could not resolve dir UIDs: one=%q two=%q", one, two)
	}
	// Reparent one -> two and two -> one, forming a 2-cycle detached from root.
	if _, err := tie.Delete(string(one), str(TieParent), singleParent(t, tie, string(one))); err != nil {
		t.Fatalf("Delete one parent: %v", err)
	}
	if _, err := tie.Delete(string(two), str(TieParent), singleParent(t, tie, string(two))); err != nil {
		t.Fatalf("Delete two parent: %v", err)
	}
	if _, err := tie.Add(string(one), str(TieParent), string(two)); err != nil {
		t.Fatalf("Add one->two: %v", err)
	}
	if _, err := tie.Add(string(two), str(TieParent), string(one)); err != nil {
		t.Fatalf("Add two->one: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rep, err := tie.Verify("", false)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(rep.Cycles) == 0 {
		t.Fatalf("no cycle reported; report=%+v", rep)
	}
}

// --- small test helpers -----------------------------------------------------

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// singleParent returns the sole parent edge value of subject, failing the test
// if there is not exactly one.
func singleParent(t *testing.T, tie *TieClient, subject string) string {
	t.Helper()
	row, err := tie.Get(subject)
	if err != nil {
		t.Fatalf("Get(%s): %v", subject, err)
	}
	parents := RowValues(row, str(TieParent))
	if len(parents) != 1 {
		t.Fatalf("%s has %d parents %v, want exactly 1", subject, len(parents), parents)
	}
	return parents[0]
}

// writeFile creates path with the given content, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// mkdir creates dir (and parents), failing the test on error.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}

// hashOf returns the content hash tie would assign to the file at path.
func hashOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer f.Close()
	h, err := metadata.HashReader(f)
	if err != nil {
		t.Fatalf("HashReader(%s): %v", path, err)
	}
	return h
}
