package putlib_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/io/putlib"
)

// TestImportSymlinkedDirectory is the §5.1 L15 guard: a symlink to a directory
// was Lstat'd, classified as a file, and hashing it failed with "is a
// directory" — which aborted the whole tree import.
func TestImportSymlinkedDirectory(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	tree := filepath.Join(base, "mytree")
	if err := os.MkdirAll(filepath.Join(tree, "real/deep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "real/deep/f.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tree, "real"), filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}

	defer chdir(t, base)()
	status := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if status.ErrorMsg != "" {
		t.Fatalf("import aborted by a symlinked directory: %s", status.ErrorMsg)
	}
	if status.LastItem.Hash == "" {
		t.Fatal("no root hash")
	}
	// The linked directory's contents must be reachable twice: once under
	// real/, once under link/.
	var dirs, files int
	for _, it := range status.UploadedItems {
		if it.MediaType == "inode/directory" {
			dirs++
		} else {
			files++
		}
	}
	if dirs < 4 || files < 2 { // mytree, real, real/deep, link, link/deep
		t.Errorf("imported %d dirs / %d files; the symlinked tree was not walked", dirs, files)
	}
}

// TestImportSymlinkLoop: following links means a link back up the tree must be
// detected, or the import recurses forever.
func TestImportSymlinkLoop(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	tree := filepath.Join(base, "mytree")
	if err := os.MkdirAll(filepath.Join(tree, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "sub/f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// A link from inside the tree back to its root.
	if err := os.Symlink(tree, filepath.Join(tree, "sub", "up")); err != nil {
		t.Fatal(err)
	}

	defer chdir(t, base)()
	done := make(chan struct{})
	var status *putlib.Status
	go func() {
		defer close(done)
		status = putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("import did not terminate: the symlink loop was not detected")
	}
	if status.ErrorMsg == "" {
		t.Error("a symlink loop was imported silently")
	} else if !strings.Contains(status.ErrorMsg, "loop") {
		t.Errorf("the loop must be reported clearly, got %q", status.ErrorMsg)
	}
}

// TestImportBrokenSymlink: a dangling link must be reported by name rather than
// surfacing as an opaque hashing failure.
func TestImportBrokenSymlink(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	tree := filepath.Join(base, "mytree")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(tree, "dangling")); err != nil {
		t.Fatal(err)
	}

	defer chdir(t, base)()
	status := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if status.ErrorMsg == "" {
		t.Fatal("a broken symlink was accepted")
	}
	if !strings.Contains(status.ErrorMsg, "dangling") {
		t.Errorf("the error must name the link, got %q", status.ErrorMsg)
	}
}
