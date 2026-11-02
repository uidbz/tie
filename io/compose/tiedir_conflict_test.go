package compose

// TreeBuilder file/dir conflict pins: the
// two structural collisions tiedir_test's name-collision tests do not cover
// — a path that is a file cannot also hold children, and a path that is a
// directory cannot also be a file. Both must error and leave the tree
// unchanged, because composition builds trees from independently-sourced specs
// whose paths can cross in exactly these shapes.

import (
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

func TestTreeBuilderFileCannotContainDir(t *testing.T) {
	b := NewTreeBuilder()
	if err := b.Add("report", metadata.DirEntry{Hash: hashOf("r1")}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := b.Add("report/section.txt", metadata.DirEntry{Hash: hashOf("s1")})
	if err == nil {
		t.Fatal("file path used as a directory: got nil, want an error")
	}
	if !strings.Contains(err.Error(), "is a file") {
		t.Errorf("error %q should say the parent is a file", err)
	}
	// The original leaf must survive the rejected Add.
	if got := b.root.children["report"].entry; got == nil || got.Hash != hashOf("r1") {
		t.Errorf("rejected Add mutated the tree: %+v", got)
	}
}

func TestTreeBuilderDirCannotBecomeFile(t *testing.T) {
	b := NewTreeBuilder()
	if err := b.Add("data/one.txt", metadata.DirEntry{Hash: hashOf("o1")}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := b.Add("data", metadata.DirEntry{Hash: hashOf("d1")})
	if err == nil {
		t.Fatal("directory path used as a file: got nil, want an error")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error %q should say the path is a directory", err)
	}
	// The directory and its child must survive the rejected Add.
	data := b.root.children["data"]
	if data == nil || data.entry != nil || data.children["one.txt"] == nil {
		t.Errorf("rejected Add mutated the tree: %+v", data)
	}
}
