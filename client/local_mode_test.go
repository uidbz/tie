package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/internal/testenv"
	"github.com/uidbz/tie/webservice"
)

func rowKeys(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Key)
	}
	sort.Strings(out)
	return out
}

// TestLocalClientMatchesServer writes a collection through a real server, stops
// it, then opens the same .tie file directly and checks that the answers match.
// The hash-shaped key exercises the blob policy: loading a server-written file
// without it panics inside tiedb.
func TestLocalClientMatchesServer(t *testing.T) {
	env := testenv.StartT(t)
	cfg := Config{
		TripleStoreURL: env.TripleStoreURL,
		Namespace:      "Collections",
		Collection:     "Main",
	}
	remote := NewTieClientFor(cfg, "")
	hash := strings.Repeat("ab", 32) // content-address shaped
	for _, tr := range [][3]string{
		{hash, "filename", "song.mp3"},
		{hash, "tag", "jazz"},
		{hash, "tag", "mellow"},
		{"other", "tag", "jazz"},
	} {
		if _, err := remote.Add(tr[0], tr[1], tr[2]); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := remote.MkTieDirAll("tie:/music"); err != nil {
		t.Fatalf("MkTieDirAll: %v", err)
	}
	dirUID, err := remote.DirUIDFromPath("tie:/music")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Add(hash, "parent", string(dirUID)); err != nil {
		t.Fatal(err)
	}

	specs := []QuerySpec{
		{Terms: []string{"jazz"}, Reverse: true, Expand: true, Limit: -1},
		{Terms: []string{"jazz", "mellow"}, Reverse: true, Expand: true, Limit: -1},
		{Terms: []string{string(dirUID)}, Reverse: true, Filter: "parent", Expand: true, Limit: -1},
		{HasRelation: "tag", Expand: true, Limit: -1},
		{Terms: []string{"jazz"}, Reverse: true, Exclude: []string{"mellow"}, Limit: -1},
	}
	type want struct {
		keys  []string
		total int
		err   error
	}
	wants := make([]want, len(specs))
	for i, spec := range specs {
		rows, total, err := remote.Query(spec)
		wants[i] = want{rowKeys(rows), total, err}
	}
	remoteDump, err := remote.Dump()
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	env.Stop() // release the per-collection lock; leave DBDir on disk

	file := filepath.Join(env.DBDir, "Collections", "Main.tie")
	localCfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatalf("ConfigForFile: %v", err)
	}
	local := NewTieClientFor(localCfg, "")
	defer local.Close()
	if !local.IsLocal() {
		t.Fatal("the client did not open in local mode")
	}
	if local.Target() != file {
		t.Errorf("Target() = %q, want %q", local.Target(), file)
	}

	for i, spec := range specs {
		rows, total, err := local.Query(spec)
		if (err != nil) != (wants[i].err != nil) {
			t.Errorf("spec %d: err = %v, server err = %v", i, err, wants[i].err)
			continue
		}
		if got, want := rowKeys(rows), wants[i].keys; !equalStrings(got, want) {
			t.Errorf("spec %d: keys = %v, server = %v", i, got, want)
		}
		if total != wants[i].total {
			t.Errorf("spec %d: total = %d, server = %d", i, total, wants[i].total)
		}
	}

	localDump, err := local.Dump()
	if err != nil {
		t.Fatalf("local Dump: %v", err)
	}
	if len(localDump.Triples) != len(remoteDump.Triples) {
		t.Errorf("dump has %d triples, server had %d", len(localDump.Triples), len(remoteDump.Triples))
	}

	// A hash-keyed Get proves the blob policy loaded the file correctly.
	row, err := local.Get(hash)
	if err != nil {
		t.Fatalf("local Get: %v", err)
	}
	if got := RowValues(row, "tag"); len(got) != 2 {
		t.Errorf("tags = %v, want jazz and mellow", got)
	}
}

// TestLocalClientIndexesEveryReverseRelation: the reverse set is derived at load
// time, not stored in the file. A server configured with only "tag" must not
// make a direct open lose "parent" lookups.
func TestLocalClientIndexesEveryReverseRelation(t *testing.T) {
	dir := t.TempDir()
	ws := webservice.NewWebservice(webservice.WebserviceConfig{
		DbPath:           dir,
		Auth:             auth.NewStore(nil, auth.RoleWrite),
		ReverseRelations: []string{"tag"},
	})
	api.RegisterAll(ws)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	srv := httptest.NewServer(mux)

	cfg := Config{TripleStoreURL: srv.URL, Namespace: "Collections", Collection: "Main"}
	remote := NewTieClientFor(cfg, "")
	uid, err := remote.MkTieDirAll("tie:/parent-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Add("child", "parent", string(uid)); err != nil {
		t.Fatal(err)
	}

	srv.Close()
	ws.Close()

	local := NewTieClientFor(Config{
		TripleStoreFile: filepath.Join(dir, "Collections", "Main.tie"),
		Collection:      "Main",
	}, "")
	defer local.Close()

	rows, total, err := local.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Filter: "parent", Limit: -1})
	if err != nil {
		t.Fatalf("parent reverse query locally: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Key != "child" {
		t.Fatalf("parent reverse query = %v (total %d), want the child", rowKeys(rows), total)
	}
}

// TestLocalClientSiblingCollections checks that In() maps a name to a sibling
// .tie file in the same directory, creates it on first write, and shares the
// backend (and therefore the lock) with the parent client.
func TestLocalClientSiblingCollections(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(mainFile, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	main := NewTieClientFor(cfg, "")
	defer main.Close()

	// An empty request pins the -f file even when the config names another
	// DefaultCollection (tie-smb and library callers rely on this).
	pinnedCfg := cfg
	pinnedCfg.DefaultCollection = "Other"
	pinned := NewTieClientFor(pinnedCfg, "")
	if got := pinned.CollectionInfo().CollectionId; got != "Main" {
		t.Errorf("empty request with DefaultCollection=Other opened %q, want Main", got)
	}
	pinned.Close()

	other := main.In("Other")
	if other == main {
		t.Fatal("In(\"Other\") returned the bound client")
	}
	if other.local != main.local {
		t.Error("a sibling collection opened a second backend instead of sharing one")
	}
	if _, err := other.Add("k", "tag", "v"); err != nil {
		t.Fatalf("Add in sibling: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Other.tie")); err != nil {
		t.Errorf("sibling file was not created: %v", err)
	}
	if row, err := main.In("Other").Get("k"); err != nil || RowFirst(row, "tag") != "v" {
		t.Errorf("sibling data = %v, %v", row, err)
	}

	// The history collection follows the same rule.
	if got := main.In(main.CollectionInfo().CollectionId + "_prev").CollectionInfo().CollectionId; got != "Main_prev" {
		t.Errorf("prev collection id = %q", got)
	}
}

// TestConfigForFileValidation: a typo must not create an empty database.
func TestConfigForFileValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := ConfigForFile(filepath.Join(dir, "missing.tie")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v, want ErrNotExist", err)
	}
	if _, err := ConfigForFile(filepath.Join(dir, "not-a-db.txt")); err == nil {
		t.Error("a non-.tie path was accepted")
	}
	if _, err := ConfigForFile(dir); err == nil {
		t.Error("a directory was accepted")
	}
	badName := filepath.Join(dir, "bad name.tie")
	if err := os.WriteFile(badName, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigForFile(badName); err == nil {
		t.Error("a non-collection-safe file name was accepted")
	}

	// A config that names a missing pinned database fails at construction
	// instead of quietly creating an empty one.
	missingCfg := Config{TripleStoreFile: filepath.Join(dir, "Missing.tie"), Collection: "Missing"}
	if tc := NewTieClientFor(missingCfg, ""); tc.Err() == nil {
		t.Error("a missing pinned database did not fail at construction")
	}
}

// TestLocalClientImportUsesFilehost proves the split the feature promises:
// import writes triples into the local file while the bytes go to the
// configured (HTTP) filehost.
func TestLocalClientImportUsesFilehost(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	host := FileHost{URL: testingFileHostURL}
	cfg.FileHosts = map[string]FileHost{"default": host}
	cfg.DefaultFileHosts = []string{"default"}

	tc := NewTieClientFor(cfg, "")
	defer tc.Close()

	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("hello local import"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tc.ImportFile(src, host, []string{"offline"}, "", TieUnknownFile); err != nil {
		t.Fatalf("ImportFile: %v", err)
	}

	rows, _, err := tc.Query(QuerySpec{Terms: []string{"offline"}, Reverse: true, Filter: "tag", Expand: true, Limit: -1})
	if err != nil {
		t.Fatalf("query imported item: %v", err)
	}
	if len(rows) != 1 || RowFirst(rows[0], "filename") != "note.txt" {
		t.Fatalf("imported rows = %+v", rows)
	}
}

// TestReadOnlyClientBesideWriter is the "query while a mount runs" case: a
// read-only client takes no lock and no writer, so it can open the same file a
// write-capable client already holds. Writes through it are refused, and the
// writer keeps working.
func TestReadOnlyClientBesideWriter(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	rw := NewTieClientFor(cfg, "")
	defer rw.Close()
	if _, err := rw.Add("k", "tag", "v"); err != nil {
		t.Fatal(err)
	}
	rw.Sync() // the reader loads on-disk state, not the writer's queue

	roCfg := cfg
	roCfg.ReadOnly = true
	ro := NewTieClientFor(roCfg, "")
	defer ro.Close()
	if err := ro.Err(); err != nil {
		t.Fatalf("read-only construction: %v", err)
	}
	row, err := ro.Get("k")
	if err != nil {
		t.Fatalf("read-only Get beside a writer: %v", err)
	}
	if RowFirst(row, "tag") != "v" {
		t.Errorf("read-only row = %v", row)
	}

	if _, err := ro.Add("k2", "tag", "v2"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "write requests are disabled") {
		t.Fatalf("write through a read-only client: err = %v", err)
	}
	if _, err := ro.MkTieDirAll("tie:/nope"); err == nil {
		t.Fatal("MkDir through a read-only client succeeded")
	}

	// The writer is unaffected: the reader held no lock.
	if _, err := rw.Add("k3", "tag", "v3"); err != nil {
		t.Fatalf("writer after a read-only open: %v", err)
	}
}

// TestDirectReverseRelationsSubset: the reverse-relations knob narrows what is
// indexed at load. A relation excluded from the subset has no reverse index, so
// a reverse query for it answers not-found; reopening with the full set finds
// it. It is the memory knob for a large database.
func TestDirectReverseRelationsSubset(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}

	base := NewTieClientFor(cfg, "")
	uid, err := base.MkTieDirAll("tie:/reverse-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Add("child", "parent", string(uid)); err != nil {
		t.Fatal(err)
	}
	base.Sync()
	base.Close()

	subset := cfg
	subset.ReadOnly = true
	subset.ReverseRelations = []string{"tag"}
	narrow := NewTieClientFor(subset, "")
	defer narrow.Close()
	_, _, err = narrow.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Filter: "parent", Limit: -1})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse parent with a tag-only index: err = %v, want ErrNotFound", err)
	}

	full := NewTieClientFor(cfg, "")
	defer full.Close()
	rows, _, err := full.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Filter: "parent", Limit: -1})
	if err != nil {
		t.Fatalf("reverse parent with the full index: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "child" {
		t.Fatalf("reverse parent rows = %v", rowKeys(rows))
	}
}

// TestLocalClientExcludesASecondOpen: a direct client holds the per-collection
// lock, so a second client (in this process or another) cannot corrupt it.
func TestLocalClientExcludesASecondOpen(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	first := NewTieClientFor(cfg, "")
	if _, err := first.Add("k", "tag", "v"); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	second := NewTieClientFor(cfg, "")
	if _, err := second.Get("k"); err == nil || !strings.Contains(err.Error(), "locked by another process") {
		t.Fatalf("second client err = %v, want a lock error", err)
	}
	second.Close()

	first.Close()

	third := NewTieClientFor(cfg, "")
	defer third.Close()
	row, err := third.Get("k")
	if err != nil {
		t.Fatalf("open after the owner closed: %v", err)
	}
	if RowFirst(row, "tag") != "v" {
		t.Errorf("data after reopen = %v", row)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
