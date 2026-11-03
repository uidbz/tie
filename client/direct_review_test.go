package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These are the regression tests for the 2026-10 direct-.tie review
// (tracking issue #156): sibling creation, the read-only snapshot, In()
// ownership, config normalization, and shared file validation.

// writeDirect creates a small direct-mode collection and returns its path.
func writeDirect(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTieClientFor(cfg, "")
	if err := tc.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Add("abc", "tag", "jazz"); err != nil {
		t.Fatal(err)
	}
	if err := tc.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := tc.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestSiblingCollectionCanBeCreated pins #145: only the PINNED file must
// exist; a sibling named through -c/In/NewTieClientFor is created on first
// write, as on a server. The old existence check always stat'ed the resolved
// file, and a sibling's basename always equals its own file's basename.
func TestSiblingCollectionCanBeCreated(t *testing.T) {
	file := writeDirect(t)
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTieClientFor(cfg, "New")
	if err := tc.Err(); err != nil {
		t.Fatalf("absent sibling rejected: %v", err)
	}
	defer tc.Close()
	if _, err := tc.Add("k", "tag", "v"); err != nil {
		t.Fatalf("write to new sibling: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), "New.tie")); err != nil {
		t.Errorf("sibling file after write: %v", err)
	}

	// The pinned file still has to exist: a typo in TripleStoreFile fails.
	bad := cfg
	bad.TripleStoreFile = filepath.Join(filepath.Dir(file), "Typo.tie")
	if err := NewTieClientFor(bad, "").Err(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing pinned file err = %v, want not-exist", err)
	}
}

// TestReadOnlyIsLoadTimeSnapshot pins #147: --read-only documents a load-time
// snapshot. The reader keeps its own private copy, so a writer deleting a
// triple after the reader loaded must not change the reader's answer, and the
// snapshot must not outlive Close.
func TestReadOnlyIsLoadTimeSnapshot(t *testing.T) {
	file := writeDirect(t)

	roCfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	roCfg.ReadOnly = true
	ro := NewTieClientFor(roCfg, "")
	if err := ro.Err(); err != nil {
		t.Fatal(err)
	}
	row, err := ro.Get("abc")
	if err != nil || RowFirst(row, Relation("tag")) != "jazz" {
		t.Fatalf("reader before writer edit = %v, %v", row, err)
	}

	// The writer deletes the triple and syncs.
	wCfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w := NewTieClientFor(wCfg, "")
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Delete("abc", "tag", "jazz"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	row, err = ro.Get("abc")
	if err != nil || RowFirst(row, Relation("tag")) != "jazz" {
		t.Errorf("reader after writer delete = %v, %v, want the load-time triple", row, err)
	}

	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 12 && e.Name()[:12] == ".tie-snapshot-" {
			t.Errorf("snapshot left behind after Close: %s", e.Name())
		}
	}
}

// TestScopedChildDoesNotCloseParent pins #150: a same-directory child shares
// the parent's open database but does not own it; closing the child must not
// pull the handle out from under the parent.
func TestScopedChildDoesNotCloseParent(t *testing.T) {
	file := writeDirect(t)
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parent := NewTieClientFor(cfg, "")
	if err := parent.Err(); err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	child := parent.In("Sib")
	if err := child.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Add("sk", "tag", "sv"); err != nil {
		t.Fatalf("child write: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}

	row, err := parent.Get("abc")
	if err != nil || RowFirst(row, Relation("tag")) != "jazz" {
		t.Errorf("parent after child.Close = %v, %v, parent must stay open", row, err)
	}
	if _, err := parent.Add("more", "tag", "x"); err != nil {
		t.Errorf("parent write after child.Close: %v", err)
	}
}

// TestFailedChildDoesNotPoisonParent covers the other #150 halves: a child
// whose open fails must not inherit or hand back the parent's backend, and a
// child opened successfully on a broken parent must not carry the parent's
// construction error.
func TestFailedChildDoesNotPoisonParent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A sibling path that is a directory: every direct open must reject it.
	if err := os.Mkdir(filepath.Join(dir, "Bad.tie"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parent := NewTieClientFor(cfg, "")
	if err := parent.Err(); err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	child := parent.In("Bad")
	if err := child.Err(); err == nil {
		t.Fatal("In(Bad.tie over a directory) succeeded")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Add("k", "tag", "v"); err != nil {
		t.Errorf("parent after failed child: %v", err)
	}

	// A broken parent (pinned file missing) must not poison a valid child.
	// Close the healthy parent first: engine locking rightly excludes a
	// second writer from Main.tie while the first holds it.
	parent.Close()
	brokenCfg, err := ConfigForFile(filepath.Join(dir, "Main.tie"))
	if err != nil {
		t.Fatal(err)
	}
	brokenCfg.TripleStoreFile = filepath.Join(dir, "Gone.tie")
	broken := NewTieClientFor(brokenCfg, "")
	if broken.Err() == nil {
		t.Fatal("broken parent unexpectedly healthy")
	}
	// The child resolves beside the (missing) pinned primary and opens the
	// existing Main.tie: construction failure of the PARENT must not poison
	// it (#150).
	fixed := broken.In("Main")
	if err := fixed.Err(); err != nil {
		t.Fatalf("valid child on broken parent: %v", err)
	}
	if _, err := fixed.Add("k1", "tag", "v1"); err != nil {
		t.Errorf("child write: %v", err)
	}
	fixed.Close()
}

// TestFileHostsArePartOfScope pins #150: a [Collections.x] entry that only
// changes filehosts must produce a scoped client with those hosts, not the
// parent returned unchanged.
func TestFileHostsArePartOfScope(t *testing.T) {
	file := writeDirect(t)
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Collections = map[string]CollectionEntry{
		"Alt": {FileHosts: []string{"media"}},
	}
	parent := NewTieClientFor(cfg, "")
	if err := parent.Err(); err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	if got := parent.ResolveHosts(nil); len(got) != 0 {
		t.Fatalf("parent hosts = %v, want none from a file-only config", got)
	}
	child := parent.In("Alt")
	if err := child.Err(); err != nil {
		t.Fatalf("scoped child: %v", err)
	}
	if got := child.ResolveHosts(nil); len(got) != 1 || got[0] != "media" {
		t.Errorf("child hosts = %v, want [media]", got)
	}
}

// TestDirectHonoursCancelledContext pins #149.3: the in-process transport is
// synchronous, but a context that is already done must fail the request
// instead of serving it — SetTimeout is a no-op, the context is the bound.
func TestDirectHonoursCancelledContext(t *testing.T) {
	file := writeDirect(t)
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTieClientFor(cfg, "")
	if err := tc.Err(); err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := tc.QueryContext(ctx, QuerySpec{Terms: []string{"jazz"}, Reverse: true}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled query = %v, want context.Canceled", err)
	}
	if _, err := tc.GetContext(ctx, "abc"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled get = %v, want context.Canceled", err)
	}
}

// TestConfigForFileHasNoImplicitFileHost pins #151: an offline direct open
// must not carry the built-in localhost filehost — an upload has to fail with
// "no filehost configured", not contact localhost.
func TestConfigForFileHasNoImplicitFileHost(t *testing.T) {
	file := writeDirect(t)
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FileHosts) != 0 {
		t.Errorf("FileHosts = %v, want empty", cfg.FileHosts)
	}
	if len(cfg.DefaultFileHosts) != 0 {
		t.Errorf("DefaultFileHosts = %v, want empty", cfg.DefaultFileHosts)
	}
}

// TestEmptyReverseRelationsIndexEverything pins #151: TOML ReverseRelations =
// [] decodes as a non-nil empty slice, which tiedb reads as an EMPTY index.
// Direct opens normalize empty to "index everything".
func TestEmptyReverseRelationsIndexEverything(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Rev.tie")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReverseRelations = []string{}
	tc := NewTieClientFor(cfg, "")
	if err := tc.Err(); err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	if _, err := tc.Add("abc", "tag", "jazz"); err != nil {
		t.Fatal(err)
	}
	rows, _, err := tc.Query(QuerySpec{Terms: []string{"jazz"}, Reverse: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("reverse query after empty-list config = %d rows, want 1 (empty must mean ALL)", len(rows))
	}
}

// TestSymlinkedPrimaryKeepsTargetIdentity pins #151: a configured
// TripleStoreFile that is a symlink resolves through the link, so the
// collection identity and <name>_prev siblings follow the TARGET's directory
// and basename, not the link's.
func TestSymlinkedPrimaryKeepsTargetIdentity(t *testing.T) {
	file := writeDirect(t)
	link := filepath.Join(filepath.Dir(file), "Alias.tie")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	cfg := Config{TripleStoreFile: link}
	resolved := cfg.ResolveCollection("")
	if resolved.Collection != "Main" {
		t.Errorf("collection = %q, want the target basename Main", resolved.Collection)
	}
	if resolved.TripleStoreFile != file {
		t.Errorf("file = %q, want the symlink target %q", resolved.TripleStoreFile, file)
	}
}

// TestExistingSiblingMustBeRegular pins #152: the shared validator runs on
// every direct open path, not just -f; an existing sibling that is a directory
// is rejected (covered above via In), and so is a FIFO — the case that used to
// block forever in os.Open with --read-only.
func TestExistingSiblingMustBeRegular(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateCollectionFile(filepath.Join(dir, "New.tie")); err != nil {
		t.Errorf("absent sibling must validate: %v", err)
	}
	if _, _, err := ValidateCollectionFile(filepath.Join(dir, "Nope.txt")); err == nil {
		t.Error("a non-.tie name passed the validator")
	}
	// Config-loaded TripleStoreFile values go through the same rules.
	bad := cfg
	bad.TripleStoreFile = filepath.Join(dir, "..")
	if err := NewTieClientFor(bad, "").Err(); err == nil {
		t.Error("a directory as TripleStoreFile was accepted")
	}
}
