package compose

import (
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// Regression guard: TreeBuilder.Add used to silently
// overwrite a leaf, so two inputs flattening to the same name lost one file
// from the input with no warning.
func TestTreeBuilderAddCollision(t *testing.T) {
	b := NewTreeBuilder()
	a := metadata.DirEntry{Hash: hashOf("aaaa"), Size: 10}
	c := metadata.DirEntry{Hash: hashOf("cccc"), Size: 20}

	if err := b.Add("data.txt", a); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	// Same hash twice stays idempotent (dedupe is the point of content addressing).
	if err := b.Add("data.txt", a); err != nil {
		t.Errorf("same-hash re-Add: got %v, want nil", err)
	}
	// Different hash under the same name must be a collision error.
	err := b.Add("data.txt", c)
	if err == nil {
		t.Fatal("different-hash re-Add: got nil, want collision error")
	}
	if !strings.Contains(err.Error(), "data.txt") {
		t.Errorf("collision error %q does not name the path", err)
	}
	// The original entry must survive the rejected Add.
	got := b.root.children["data.txt"].entry
	if got == nil || got.Hash != a.Hash {
		t.Errorf("rejected Add mutated the tree: entry=%+v, want hash %s", got, a.Hash)
	}
}

// Nested paths collide on their full path, not just top-level names.
func TestTreeBuilderAddCollisionNested(t *testing.T) {
	b := NewTreeBuilder()
	if err := b.Add("sub/dir/file", metadata.DirEntry{Hash: hashOf("bbbb")}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := b.Add("sub/dir/file", metadata.DirEntry{Hash: hashOf("dddd")}); err == nil {
		t.Fatal("nested different-hash re-Add: got nil, want collision error")
	}
}
