package client

import (
	"fmt"
	"testing"
)

// TestReadTieDirBeyondDefaultLimit is the regression for §3.1: an unlimited
// query (Limit 0) silently got the server's 1000-triple default, so directory
// listings, version lists and tag views missed everything past the 1000th
// child — which in turn made RenameDir leave stale path triples on the missed
// descendants.
func TestReadTieDirBeyondDefaultLimit(t *testing.T) {
	tie := freshVerifyClient(t)

	const children = 1500
	dirUID := DirUID(fmt.Sprintf("%064x", 1))

	b := tie.NewBatch()
	b.Add(string(dirUID), string(RelType), str(TieDirectory))
	b.Add(string(dirUID), string(RelPath), "/big")
	for i := 0; i < children; i++ {
		hash := fmt.Sprintf("%064x", i+2)
		b.Add(hash, string(RelParent), string(dirUID))
		b.Add(hash, string(RelType), "file")
		b.Add(hash, string(RelFilename), fmt.Sprintf("f%04d.txt", i))
	}
	if _, err := tie.Batch(b); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dir, err := ReadTieDir(tie, dirUID)
	if err != nil {
		t.Fatalf("ReadTieDir: %v", err)
	}
	if len(dir.Files) != children {
		t.Errorf("ReadTieDir returned %d files, want all %d (the 1000-triple default truncated the listing)", len(dir.Files), children)
	}

	// fileChildren (reconciliation / WriteFile) must see them all too.
	kids, err := fileChildren(tie, dirUID)
	if err != nil {
		t.Fatalf("fileChildren: %v", err)
	}
	if len(kids) != children {
		t.Errorf("fileChildren returned %d, want %d", len(kids), children)
	}
}

// TestQueryLimitZeroIsUnlimited pins the client-side normalization: 0 and
// negative both mean unlimited, and an explicit limit still limits.
func TestQueryLimitZeroIsUnlimited(t *testing.T) {
	tie := freshVerifyClient(t)

	const seeded = 1200
	tag := "limit-zero-tag"
	seed := tie.NewBatch()
	for i := 0; i < seeded; i++ {
		seed.Add(fmt.Sprintf("lz-%04d", i), "tag", tag)
	}
	if _, err := tie.Batch(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	all, total, err := tie.Query(QuerySpec{Terms: []string{tag}, Reverse: true})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(all) < seeded {
		t.Errorf("unlimited query returned %d rows (total %d), want at least %d", len(all), total, seeded)
	}

	limited, _, err := tie.Query(QuerySpec{Terms: []string{tag}, Reverse: true, Limit: 10})
	if err != nil {
		t.Fatalf("limited query: %v", err)
	}
	if len(limited) != 10 {
		t.Errorf("explicit limit returned %d rows, want 10", len(limited))
	}
}
