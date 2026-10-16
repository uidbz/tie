package putlib_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/metadata"
)

// TestRoundTripAwkwardFilenames is the §4.8 guard. Linux allows TAB and LF in a
// filename, and putlib used to write the name raw into a TAB/LF-delimited
// manifest line: the entry then failed to parse in every reader, so the file
// vanished from checkout and — worse — the reaper's dirChildren skipped it and
// could reap a blob that was still referenced.
func TestRoundTripAwkwardFilenames(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	treeDir := filepath.Join(base, "mytree")
	if err := os.MkdirAll(treeDir, 0755); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"with\ttab.txt",
		"with\nlf.txt",
		"percent%25.txt",
		"100%.mp3",
		"plain.txt",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(treeDir, n), []byte("body:"+n), 0644); err != nil {
			t.Fatal(err)
		}
	}

	defer chdir(t, base)()
	status := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if status.ErrorMsg != "" {
		t.Fatalf("upload: %s", status.ErrorMsg)
	}
	rootHash := status.LastItem.Hash

	// The manifest itself must carry one parseable entry per child, with the
	// original name recovered. This is what the reaper walks.
	manifest := fetch(t, srv.URL, rootHash)
	lines := strings.Split(strings.TrimPrefix(manifest, metadata.DirHeader), "\n")
	var gotNames []string
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		e, ok := metadata.ParseDirLine(ln)
		if !ok {
			t.Errorf("manifest line %q does not parse", ln)
			continue
		}
		gotNames = append(gotNames, e.Filename)
	}
	if len(gotNames) != len(names) {
		t.Fatalf("manifest holds %d entries (%v), want %d", len(gotNames), gotNames, len(names))
	}

	// And a checkout must rebuild the tree with the exact names.
	dest := filepath.Join(base, "checkout")
	if err := getlib.DownloadFile(nil, srv.URL, rootHash, dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dest, n))
		if err != nil {
			t.Errorf("checkout is missing %q: %v", n, err)
			continue
		}
		if string(b) != "body:"+n {
			t.Errorf("%q came back as %q", n, b)
		}
	}
}

// fetch reads one blob from the in-memory filehost.
func fetch(t *testing.T, url, hash string) string {
	t.Helper()
	resp, err := http.Get(url + "/" + hash)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestUploadRejectsUnrepresentableName: a name that cannot appear in a manifest
// at all (a backslash is a path separator on Windows) must be refused up front
// rather than uploaded and then dropped by every reader, which would leave the
// blob unreferenced and unprotected by the reaper.
func TestUploadRejectsUnrepresentableName(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	treeDir := filepath.Join(base, "mytree")
	if err := os.MkdirAll(treeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(treeDir, `back\slash.txt`), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(treeDir, "ok.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	defer chdir(t, base)()
	status := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if status.ErrorMsg == "" {
		t.Fatal("a filename that cannot be stored in a manifest was accepted silently")
	}
	if !strings.Contains(status.ErrorMsg, `back\slash.txt`) {
		t.Errorf("the error must name the offending file, got %q", status.ErrorMsg)
	}
}
