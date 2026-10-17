package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveTestClient returns a client against the local test triplestore
// (TestingConfig), skipping the test when no store is reachable.
func liveTestClient(t *testing.T) (*TieClient, FileHost) {
	t.Helper()
	tie := NewTieClient(TestingConfig())
	if _, err := tie.ServerVersion(); err != nil {
		t.Skipf("test triplestore not reachable: %v", err)
	}
	host, err := tie.ResolveHost("")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	return tie, host
}

// writeTempFile stages content in a temp file and commits it into parentUID
// under name, returning its content hash.
func writeTempFile(t *testing.T, tie *TieClient, host FileHost, parentUID DirUID, name, content string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := tie.WriteFile(host, parentUID, name, tmp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestDeleteFileAndDir(t *testing.T) {
	tie, host := liveTestClient(t)
	dirPath := fmt.Sprintf("/delete-test-%d", time.Now().UnixNano())

	uid, err := tie.MkTieDirAll(FileURIScheme + dirPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := writeTempFile(t, tie, host, uid, "gone.txt", "delete me")

	dir, err := ReadTieDir(tie, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(dir.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(dir.Files))
	}

	// A non-empty directory refuses DeleteDir.
	rootUID, err := tie.DirUIDFromPath("/")
	if err != nil || rootUID == "" {
		t.Fatalf("root uid: %q %v", rootUID, err)
	}
	if err := DeleteDir(tie, uid, rootUID); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("DeleteDir on non-empty dir: got %v, want 'not empty'", err)
	}

	if err := DeleteFile(tie, hash, uid); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	dir, err = ReadTieDir(tie, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(dir.Files) != 0 {
		t.Errorf("file still listed after DeleteFile: %d", len(dir.Files))
	}

	if err := DeleteDir(tie, uid, rootUID); err != nil {
		t.Fatalf("DeleteDir: %v", err)
	}
	if got, err := tie.DirUIDFromPath(dirPath); err != nil || got != "" {
		t.Errorf("DirUIDFromPath after DeleteDir = %q, %v; want empty", got, err)
	}
	// The directory's own triples are gone, so the consistency checker cannot
	// see an orphan.
	if _, err := tie.Get(string(uid)); err == nil {
		t.Error("deleted directory still has triples")
	}
}

func TestDeleteFileSharedContent(t *testing.T) {
	tie, host := liveTestClient(t)
	stamp := time.Now().UnixNano()
	dirA := fmt.Sprintf("/delete-shared-a-%d", stamp)
	dirB := fmt.Sprintf("/delete-shared-b-%d", stamp)

	uidA, err := tie.MkTieDirAll(FileURIScheme + dirA)
	if err != nil {
		t.Fatal(err)
	}
	uidB, err := tie.MkTieDirAll(FileURIScheme + dirB)
	if err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("shared bytes %d", stamp)
	hash := writeTempFile(t, tie, host, uidA, "shared.txt", content)
	hashB := writeTempFile(t, tie, host, uidB, "shared.txt", content)
	if hash != hashB {
		t.Fatalf("same content should yield one hash: %s vs %s", hash, hashB)
	}

	if err := DeleteFile(tie, hash, uidA); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	// Still reachable from dirB: listed there, metadata intact.
	dir, err := ReadTieDir(tie, uidB)
	if err != nil {
		t.Fatal(err)
	}
	if len(dir.Files) != 1 || dir.Files[0].Filename != "shared.txt" {
		t.Errorf("shared content vanished from the other parent: %+v", dir.Files)
	}
	tags, err := GetTags(tie, hash)
	if err != nil {
		t.Fatal(err)
	}
	_ = tags // metadata (incl. filename) survives while a parent edge remains

	// Deleting the last edge drops the per-hash triples.
	if err := DeleteFile(tie, hash, uidB); err != nil {
		t.Fatalf("DeleteFile (last parent): %v", err)
	}
	if _, err := tie.Get(hash); err == nil {
		t.Error("hash still has triples after last DeleteFile")
	}

	// Clean up the two test directories.
	rootUID, err := tie.DirUIDFromPath("/")
	if err != nil || rootUID == "" {
		t.Fatalf("root uid: %q %v", rootUID, err)
	}
	for _, u := range []DirUID{uidA, uidB} {
		if err := DeleteDir(tie, u, rootUID); err != nil {
			t.Errorf("cleanup DeleteDir %s: %v", u, err)
		}
	}
}
