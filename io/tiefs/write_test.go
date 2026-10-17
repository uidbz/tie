package tiefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
)

// deadTree is a tree whose triplestore and filehost are unreachable: every
// commit fails, which is the case §4.9 is about.
func deadTree(t *testing.T) *Tree {
	t.Helper()
	cfg := client.Config{
		TripleStoreURL:   "http://127.0.0.1:1",
		Namespace:        "nstest",
		Collection:       "writetesting",
		DefaultFileHosts: []string{"default"},
		FileHosts:        map[string]client.FileHost{"default": {URL: "http://127.0.0.1:1"}},
	}
	tc := client.NewTieClient(cfg)
	tr := NewTree(tc, tc.Config.FileHosts["default"], 0, false)
	t.Cleanup(func() { tr.Close() })
	return tr
}

// TestFailedCommitKeepsTheBytes is the §4.9 guard: Close committed and then
// always deleted the staging file, so a write that could not reach the
// triplestore or the filehost destroyed the user's bytes — silently, because
// most applications ignore the error from close()/release().
func TestFailedCommitKeepsTheBytes(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TIE_SPOOL_DIR", spool)
	tr := deadTree(t)

	h, err := tr.NewWriteHandle("/music", "song.txt", client.File{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteAt([]byte("precious bytes"), 0); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err == nil {
		t.Fatal("a commit against an unreachable triplestore reported no error")
	}

	entries, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the spool holds %d files, want the one that failed: %v", len(entries), entries)
	}
	data, err := os.ReadFile(filepath.Join(spool, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "precious bytes" {
		t.Errorf("the spooled bytes are %q, want the bytes that were written", data)
	}
	// The name has to say what the file was and where it belonged, and the
	// error has to say where it went.
	if !strings.HasPrefix(entries[0].Name(), "music_") || !strings.Contains(entries[0].Name(), "song.txt") {
		t.Errorf("spool name %q does not carry the intended path and file name", entries[0].Name())
	}
}

// TestCleanCloseKeepsTheSpoolEmpty: the spool is for failures only. A handle
// with nothing staged has no user data to protect.
func TestCleanCloseKeepsTheSpoolEmpty(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TIE_SPOOL_DIR", spool)
	tr := deadTree(t)

	h, err := tr.NewWriteHandle("/music", "empty.txt", client.File{})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close() // the commit fails, but nothing was ever written

	entries, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a handle with no staged bytes left %v in the spool", entries)
	}
}

// TestSpoolSurvivesTheStore: Store.Close removes the staging directory, so a
// spooled file must live outside it.
func TestSpoolSurvivesTheStore(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TIE_SPOOL_DIR", spool)
	tr := deadTree(t)

	h, err := tr.NewWriteHandle("/", "note.txt", client.File{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteAt([]byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	if err := tr.Store.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("closing the store took the spooled bytes with it: %v", entries)
	}
}
