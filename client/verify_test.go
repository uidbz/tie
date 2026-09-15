package client

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// verify_test.go exercises Verify and RepairOrphans against a real triplestore
// (test-env/start.sh), using the isolated "testing" collection. Each test
// drops the collection first so it runs against a known-empty tree regardless
// of what other tests left behind. The triplestore must be current (the tests
// build a path tree with ImportDir and depend on the MissingRelation-free
// reverse tie-type queries Verify uses).

// verifyTestConfig returns a Config bound to the test-env triplestore/filehost
// (2161/2162) and the isolated "testing" collection, so these tests neither
// touch the operator's live Main collection nor require system services on
// 1161/1162. Set TIE_TEST_WEBSERVICE / TIE_TEST_FILEHOST to override.
func verifyTestConfig() Config {
	c := TestingConfig()
	c.Collection = "verifytesting"
	if ws := os.Getenv("TIE_TEST_WEBSERVICE"); ws != "" {
		c.TripleStoreURL = ws
	} else {
		c.TripleStoreURL = "http://localhost:2161"
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
// collection on the test-env triplestore.
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

// TestCheckIndexRoundTrip exercises the CheckIndex request end to end: a
// freshly imported tree must report a consistent index with matching entry
// counts in plain, deep and repair modes, and Delete must remain able to clear
// a triple after the check has run (the check takes the collection's write
// lock and must release it).
func TestCheckIndexRoundTrip(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "hello")
	writeFile(t, dir+"/b.txt", "world")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}

	for _, mode := range []struct {
		name         string
		deep, repair bool
	}{{"plain", false, false}, {"deep", true, false}, {"repair", false, true}} {
		rep, err := tie.CheckIndex("", mode.deep, mode.repair)
		if err != nil {
			t.Fatalf("%s: CheckIndex: %v", mode.name, err)
		}
		if rep.Problems() != 0 || rep.Repaired != 0 {
			t.Errorf("%s: fresh import reported index problems: %+v", mode.name, rep)
		}
		if rep.ForwardEntries == 0 || rep.ReverseEntries == 0 || rep.ReverseEntries > rep.ForwardEntries {
			t.Errorf("%s: implausible counts fwd=%d rev=%d", mode.name, rep.ForwardEntries, rep.ReverseEntries)
		}
		if rep.Deep != mode.deep {
			t.Errorf("%s: Deep flag not echoed", mode.name)
		}
	}

	// Writers must not stay blocked after the check.
	if _, err := tie.Add("idxprobe", "tag", "x"); err != nil {
		t.Fatalf("Add after CheckIndex: %v", err)
	}
	if _, err := tie.Delete("idxprobe", "tag", "x"); err != nil {
		t.Fatalf("Delete after CheckIndex: %v", err)
	}
}

// TestRepairTree exercises the destructive fixes (`tie verify --fix`) end to
// end on one damaged tree: a file whose parent edge points at a ghost node
// (dir-typed, no path) that hangs off a live directory, a bare file node whose
// blob is gone, and an imported file stripped of its size/media-type. The plan
// must be reported without changes in dry-run mode; applying it must leave a
// clean tree: the child re-parented to the ghost's live ancestor, the ghost and
// the blobless leftover deleted, and the metadata re-derived from the blob.
func TestRepairTree(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "repair me please")
	writeFile(t, dir+"/b.txt", "i will dangle")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "", "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	root, err := tie.DirUIDFromPath(FileURIScheme + dir)
	if err != nil || root == "" {
		t.Fatalf("DirUIDFromPath: uid=%q err=%v", root, err)
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	mustAdd := func(what string, k, v1, v2 string) {
		t.Helper()
		_, err := tie.Add(k, v1, v2)
		must(what, err)
	}
	mustDel := func(what string, k, v1, v2 string) {
		t.Helper()
		_, err := tie.Delete(k, v1, v2)
		must(what, err)
	}

	// 1. Ghost: a dir-typed node with no path, parented to the live root, and
	//    b.txt re-pointed at it (so b's parent edge is dangling).
	ghost := "feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface"
	mustAdd("add ghost tie-type", ghost, str(TieTypeProperty), str(TieDirectory))
	mustAdd("add ghost parent", ghost, str(TieParent), string(root))
	b := hashOf(t, dir+"/b.txt")
	mustDel("cut b parent", b, str(TieParent), string(root))
	mustAdd("dangle b", b, str(TieParent), ghost)

	// 2. Blobless leftover: tie-type file + parent, no name, no blob uploaded.
	bare := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mustAdd("add bare tie-type", bare, str(TieTypeProperty), str(TieFile))
	mustAdd("add bare parent", bare, str(TieParent), string(root))

	// 3. Metadata gap on a real file: strip size and media-type.
	a := hashOf(t, dir+"/a.txt")
	row, err := tie.Get(a)
	must("get a", err)
	for _, rel := range []string{str(TieFilesize), str(TieMediaType)} {
		for _, v := range RowValues(row, rel) {
			mustDel("strip "+rel, a, rel, v)
		}
	}
	must("sync", tie.Sync())

	rep, err := tie.Verify("", false)
	must("verify", err)
	if len(rep.DanglingParentRefs) != 1 || rep.DanglingParentRefs[0].Child != b || rep.DanglingParentRefs[0].Parent != ghost {
		t.Fatalf("DanglingParentRefs = %+v, want b -> ghost", rep.DanglingParentRefs)
	}
	if len(rep.MissingMetadata) < 2 {
		t.Fatalf("MissingMetadata = %+v, want at least the bare node and a.txt", rep.MissingMetadata)
	}

	// Dry run: a plan, and nothing changes.
	plan, err := tie.RepairTree("", rep, RepairOptions{})
	must("plan", err)
	if len(plan.Planned) == 0 || plan.Applied != 0 {
		t.Fatalf("dry run: planned=%d applied=%d", len(plan.Planned), plan.Applied)
	}
	rep2, err := tie.Verify("", false)
	must("verify after dry run", err)
	if rep2.Problems() != rep.Problems() {
		t.Fatalf("dry run changed the tree: %d -> %d problems", rep.Problems(), rep2.Problems())
	}

	// Apply.
	var journal bytes.Buffer
	res, err := tie.RepairTree("", rep, RepairOptions{Apply: true, Journal: &journal})
	must("apply", err)
	if res.Applied != len(res.Planned) || res.Applied == 0 {
		t.Fatalf("applied %d of %d planned", res.Applied, len(res.Planned))
	}
	if lines := strings.Count(journal.String(), "\n"); lines != res.Applied {
		t.Errorf("journal has %d lines, want %d", lines, res.Applied)
	}

	rep3, err := tie.Verify("", false)
	must("verify after apply", err)
	if rep3.Problems() != 0 {
		t.Fatalf("tree still has problems after RepairTree: %+v", rep3)
	}
	if got := singleParent(t, tie, b); got != string(root) {
		t.Errorf("b re-parented to %s, want live ancestor %s", got, root)
	}
	if _, err := tie.Get(ghost); err == nil {
		t.Error("ghost node still exists")
	}
	if _, err := tie.Get(bare); err == nil {
		t.Error("blobless leftover still exists")
	}
	row, err = tie.Get(a)
	must("get a after", err)
	if RowFirst(row, str(TieFilesize)) != strconv.Itoa(len("repair me please")) {
		t.Errorf("a.txt filesize = %q, want %d", RowFirst(row, str(TieFilesize)), len("repair me please"))
	}
	if RowFirst(row, str(TieMediaType)) == "" {
		t.Error("a.txt media-type not re-derived")
	}
}
