package getlib

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// A download that stops early — a server closing the body, a dropped
// connection, ENOSPC — used to leave a truncated file at the final path, with
// the checksum error arriving only after the bytes were already in place. Callers
// that cache materialized blobs by content address (tie-gui, mount, `download`)
// treat "the file exists" as "the file is complete", so one interrupted transfer
// poisoned them permanently.

// shortServer answers GET /<hash> with the first n bytes of the real content,
// ending the response cleanly: io.Copy sees EOF, not an error.
func shortServer(content string, n int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content[:n]))
	}))
}

func hashOf(t *testing.T, content string) string {
	t.Helper()
	h, err := metadata.HashReader(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// onlyFiles lists what a download left in dest, so a test can insist that
// nothing but the expected files is there — no staged temp file, no truncated
// file at the final path.
func onlyFiles(t *testing.T, dest string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = true
		return nil
	})
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTruncatedDownloadLeavesNoFile(t *testing.T) {
	content := "the complete blob contents, served only in part to the client"
	hash := hashOf(t, content)
	srv := shortServer(content, 10)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	if err := DownloadFile(nil, srv.URL, hash, dest, nil); err == nil {
		t.Fatal("a short transfer must be an error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the truncated download left a file at %s; a caller that caches by hash would keep it forever", dest)
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("the failed download left files behind: %v", keys(left))
	}
}

func TestAbortedTransferLeavesNoFile(t *testing.T) {
	content := "bytes the connection never finishes delivering to the disk"
	hash := hashOf(t, content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise the whole body, deliver part of it, then drop the connection:
		// the client sees an unexpected EOF, which io.Copy reports as an error.
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write([]byte(content[:12]))
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	if err := DownloadFile(nil, srv.URL, hash, dest, nil); err == nil {
		t.Fatal("a dropped connection must be an error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the aborted download left a file at %s", dest)
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("the failed download left files behind: %v", keys(left))
	}
}

func TestTamperedDownloadLeavesNoFile(t *testing.T) {
	hash := hashOf(t, "legitimate content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("evil replacement bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	if err := DownloadFile(nil, srv.URL, hash, dest, nil); err != ErrChecksum {
		t.Fatalf("expected ErrChecksum, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the tampered download left a file at %s", dest)
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("the failed download left files behind: %v", keys(left))
	}
}

// TestTreeDownloadFailureLeavesNoTruncatedFile covers the directory case: a tree
// commits file by file, so a failure deep in the tree may leave the files that
// completed — what it must never leave is a half-written file that looks done.
func TestTreeDownloadFailureLeavesNoTruncatedFile(t *testing.T) {
	bs := newBlobServer()
	good := bs.put(t, "a file that transfers completely")
	badContent := "this file is served short and fails its checksum"
	bad := hashOf(t, badContent)
	bs.blobs[bad] = []byte(badContent[:6]) // the server will answer with 6 bytes

	sub := bs.put(t, metadata.DirHeader+
		metadata.DirEntry{Hash: bad, Filename: "bad.txt", Size: len(badContent)}.Line())

	root := bs.put(t, metadata.DirHeader+
		metadata.DirEntry{Hash: good, Filename: "good.txt", Size: len("a file that transfers completely")}.Line()+
		metadata.DirEntry{Hash: sub, Filename: "sub", Size: 0, Head: []byte(metadata.DirHeader)}.Line())

	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := DownloadFile(nil, srv.URL, root, dest, nil); err == nil {
		t.Fatal("expected a checksum error for the short entry")
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", "bad.txt")); !os.IsNotExist(err) {
		t.Error("the short entry left a truncated file at its final path")
	}
	for name := range onlyFiles(t, dest) {
		if name != "good.txt" {
			t.Errorf("unexpected file left by the failed download: %s", name)
		}
	}
}

// TestSuccessfulDownloadLeavesNoStagingFiles: the fix must not litter. A good
// download of a tree ends with exactly the tree's files, and the destination
// directory holds no temp file from the staging step.
func TestSuccessfulDownloadLeavesNoStagingFiles(t *testing.T) {
	bs := newBlobServer()
	a := bs.put(t, "first file contents")
	bContent := strings.Repeat("nested file, larger than one copy buffer ", 200)
	b := bs.put(t, bContent)
	cContent := "three levels deep"
	c := bs.put(t, cContent)

	deeper := bs.put(t, metadata.DirHeader+
		metadata.DirEntry{Hash: c, Filename: "c.txt", Size: len(cContent)}.Line())

	sub := bs.put(t, metadata.DirHeader+
		metadata.DirEntry{Hash: b, Filename: "b.txt", Size: len(bContent)}.Line()+
		metadata.DirEntry{Hash: deeper, Filename: "deeper", Size: 0, Head: []byte(metadata.DirHeader)}.Line())

	root := bs.put(t, metadata.DirHeader+
		metadata.DirEntry{Hash: a, Filename: "a.txt", Size: len("first file contents")}.Line()+
		metadata.DirEntry{Hash: sub, Filename: "sub", Size: 0, Head: []byte(metadata.DirHeader)}.Line())

	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	if err := DownloadFile(nil, srv.URL, root, dest, nil); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"a.txt": true, "sub/b.txt": true, "sub/deeper/c.txt": true}
	got := onlyFiles(t, dest)
	for name := range want {
		if !got[name] {
			t.Errorf("%s was not written", name)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("the download left an unexpected file: %s", name)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "sub", "b.txt")); err != nil || !strings.HasPrefix(string(data), "nested file") {
		t.Errorf("sub/b.txt content wrong: %v %q", err, first(string(data), 24))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
