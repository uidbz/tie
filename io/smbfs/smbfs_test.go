package smbfs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"
	"testing"
	"time"

	gofs "github.com/go-filesystems/interface"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// liveShare returns a Share over the local test triplestore (client.
// TestingConfig), skipping the test when no store is reachable - the same
// pattern as client/delete_test.go. The tree is closed on cleanup.
func liveShare(t *testing.T) *Share {
	t.Helper()
	tie := client.NewTieClient(client.TestingConfig())
	if _, err := tie.ServerVersion(); err != nil {
		t.Skipf("test triplestore not reachable: %v", err)
	}
	host, err := tie.ResolveHost("")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	tree := tiesfs.NewTree(tie, host, 1, false)
	t.Cleanup(func() { tree.Close() })
	return NewShare(tree)
}

// testBase returns a unique fresh directory path under the tie root and
// registers cleanup that empties and removes it.
func testBase(t *testing.T, s *Share) string {
	t.Helper()
	base := fmt.Sprintf("/smbfs-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleanupBelow(s.tree, base) })
	return base
}

// cleanupBelow recursively removes the tie path tree rooted at p (best
// effort; leftovers from a failed test are harmless in the test store).
func cleanupBelow(tree *tiesfs.Tree, p string) {
	dir, err := tree.PathDir(p)
	if err != nil {
		return
	}
	for _, sub := range dir.SubDirs {
		for _, sp := range sub.Paths {
			cleanupBelow(tree, tiesfs.ChildPath(p, tiesfs.BaseName(sp)))
		}
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		tree.DeleteFile(p, f.Filename)
	}
	parent := "/"
	if i := strings.LastIndex(p, "/"); i > 0 {
		parent = p[:i]
	}
	tree.DeleteDir(parent, tiesfs.BaseName(p))
}

func mustStat(t *testing.T, s *Share, p string) gofs.Stat {
	t.Helper()
	st, err := s.Stat(p)
	if err != nil {
		t.Fatalf("Stat(%q): %v", p, err)
	}
	return st
}

func mustNotExist(t *testing.T, s *Share, p string) {
	t.Helper()
	if _, err := s.Stat(p); !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("Stat(%q) = %v, want fs.ErrNotExist", p, err)
	}
}

func TestStatAndListViews(t *testing.T) {
	s := liveShare(t)

	root := mustStat(t, s, "/")
	if root.Mode()&0xF000 != 0x4000 {
		t.Errorf("root mode = 0%o, want a directory", root.Mode())
	}
	names := listNames(t, s, "/")
	for _, want := range []string{viewQuery, viewFiles, viewTags} {
		if !contains(names, want) {
			t.Errorf("ListDir(/) = %v, want %q", names, want)
		}
	}

	// /query helper files.
	readme := mustStat(t, s, "/query/"+tiesfs.ReadmeName)
	if readme.Mode()&0o222 != 0 {
		t.Errorf("README mode = 0%o, want read-only", readme.Mode())
	}
	if readme.Size() != uint64(len(tiesfs.ReadmeText)) {
		t.Errorf("README size = %d, want %d", readme.Size(), len(tiesfs.ReadmeText))
	}
	data, err := s.ReadFile("/query/" + tiesfs.ReadmeName)
	if err != nil || string(data) != tiesfs.ReadmeText {
		t.Errorf("ReadFile(README): %q, %v", data, err)
	}
	if _, err := s.Stat("/query/" + tiesfs.TagListName); err != nil {
		t.Errorf("Stat(tags listing): %v", err)
	}
	if _, err := s.Stat("/query/" + tiesfs.TypeListName); err != nil {
		t.Errorf("Stat(types listing): %v", err)
	}
	if !contains(listNames(t, s, "/query"), tiesfs.ReadmeName) {
		t.Errorf("ListDir(/query) missing README")
	}

	mustNotExist(t, s, "/nope")
	mustNotExist(t, s, "/query/no-such-helper/deeper")
}

func TestPathTreeWriteFlow(t *testing.T) {
	s := liveShare(t)
	base := testBase(t, s)
	files := "/files" + base

	// MkDir materializes the chain.
	if err := s.MkDir(files+"/sub", 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}
	st := mustStat(t, s, files)
	if st.Mode() != 0o040755 {
		t.Errorf("files dir mode = 0%o, want 040755", st.Mode())
	}
	// CREATE flow: WriteFile(p,nil) stages; Stat and ListDir must see the
	// pending file before any bytes arrive.
	p := files + "/sub/f.txt"
	if err := s.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(p,nil): %v", err)
	}
	if st := mustStat(t, s, p); st.Size() != 0 || st.Mode() != 0o100644 {
		t.Errorf("pending Stat: size %d mode 0%o, want 0/0644", st.Size(), st.Mode())
	}
	if !contains(listNames(t, s, files+"/sub"), "f.txt") {
		t.Fatal("pending create not listed")
	}

	// OpenFile must hand back the staged handle as a WritableFile.
	f, err := s.OpenFile(p)
	if err != nil {
		t.Fatalf("OpenFile(pending): %v", err)
	}
	w, ok := f.(gofs.WritableFile)
	if !ok {
		t.Fatalf("OpenFile(pending) is not a WritableFile")
	}
	if n, err := w.WriteAt([]byte("hello"), 0); err != nil || n != 5 {
		t.Fatalf("WriteAt: %d %v", n, err)
	}
	if err := w.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if f.Size() != 5 {
		t.Errorf("handle Size = %d, want 5", f.Size())
	}
	back := make([]byte, 5)
	if n, err := f.ReadAt(back, 0); n != 5 || (err != nil && !errors.Is(err, io.EOF)) {
		t.Fatalf("read-back through handle: %d %v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Committed: the bytes are readable through the path again.
	if st := mustStat(t, s, p); st.Size() != 5 {
		t.Errorf("committed Stat size = %d, want 5", st.Size())
	}
	if data, err := s.ReadFile(p); err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}

	// Truncate-and-rewrite (the OVERWRITE_IF disposition).
	if err := s.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(p,nil) truncate: %v", err)
	}
	if st := mustStat(t, s, p); st.Size() != 0 {
		t.Errorf("truncated Stat size = %d, want 0", st.Size())
	}
	f2, err := s.OpenFile(p)
	if err != nil {
		t.Fatalf("OpenFile(truncated): %v", err)
	}
	if _, err := f2.(gofs.WritableFile).WriteAt([]byte("bye"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := f2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if data, err := s.ReadFile(p); err != nil || string(data) != "bye" {
		t.Fatalf("ReadFile after rewrite = %q, %v", data, err)
	}

	// Lazy open + first write seeds from stored content (partial in-place edit).
	f3, err := s.OpenFile(p)
	if err != nil {
		t.Fatalf("OpenFile(lazy): %v", err)
	}
	if _, err := f3.(gofs.WritableFile).WriteAt([]byte("X"), 0); err != nil {
		t.Fatalf("WriteAt on lazy handle: %v", err)
	}
	if err := f3.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if data, err := s.ReadFile(p); err != nil || string(data) != "Xye" {
		t.Fatalf("ReadFile after partial edit = %q, %v", data, err)
	}

	// Whole-file fallback (WriteFile with data).
	if err := s.WriteFile(files+"/sub/whole.txt", []byte("all at once"), 0o644); err != nil {
		t.Fatalf("WriteFile(data): %v", err)
	}
	if data, err := s.ReadFile(files + "/sub/whole.txt"); err != nil || string(data) != "all at once" {
		t.Fatalf("ReadFile(whole) = %q, %v", data, err)
	}

	// Rename replaces within /files (the lib ignores ReplaceIfExists).
	if err := s.Rename(p, files+"/sub/g.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	mustNotExist(t, s, p)
	if data, err := s.ReadFile(files + "/sub/g.txt"); err != nil || string(data) != "Xye" {
		t.Fatalf("ReadFile(renamed) = %q, %v", data, err)
	}

	// DeleteFile unlinks; DeleteDir refuses a non-empty dir with fs.ErrExist
	// (the closest SMB status) and accepts an empty one.
	if err := s.DeleteFile(files + "/sub/g.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	mustNotExist(t, s, files+"/sub/g.txt")
	if err := s.DeleteDir(files + "/sub"); !errors.Is(err, iofs.ErrExist) {
		t.Fatalf("DeleteDir(non-empty) = %v, want fs.ErrExist", err)
	}
	if err := s.DeleteFile(files + "/sub/whole.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if err := s.DeleteDir(files + "/sub"); err != nil {
		t.Fatalf("DeleteDir(empty): %v", err)
	}
	mustNotExist(t, s, files+"/sub")
}

func TestReadOnlyViews(t *testing.T) {
	s := liveShare(t)
	base := testBase(t, s)
	if err := s.MkDir("/files"+base+"/adir", 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}

	for _, p := range []string{"/query/new", "/query/README", "/query/tags/x"} {
		if err := s.WriteFile(p, nil, 0o644); !errors.Is(err, iofs.ErrPermission) {
			t.Errorf("WriteFile(%q) = %v, want fs.ErrPermission", p, err)
		}
	}
	if err := s.MkDir("/query/somedir", 0o755); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("MkDir(/query/...) = %v, want fs.ErrPermission", err)
	}
	if err := s.DeleteFile("/query/README"); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("DeleteFile(/query/README) = %v, want fs.ErrPermission", err)
	}
	if err := s.Rename("/query/README", "/query/README2"); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("Rename(/query/...) = %v, want fs.ErrPermission", err)
	}
	// Writing onto a directory reports a collision, not permission.
	for _, p := range []string{"/files", "/files" + base, "/files" + base + "/adir"} {
		if err := s.WriteFile(p, nil, 0o644); !errors.Is(err, iofs.ErrExist) {
			t.Errorf("WriteFile(%q) = %v, want fs.ErrExist", p, err)
		}
	}
	// /tags and /files directories cannot be mkdir'd or rmdir'd through the
	// fixed top level, and /tags leaves are not deletable.
	if err := s.MkDir("/tags/x", 0o755); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("MkDir(/tags/x) = %v, want fs.ErrPermission", err)
	}
	if err := s.DeleteFile("/files"); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("DeleteFile(/files) = %v, want fs.ErrPermission", err)
	}
}

func TestQueryView(t *testing.T) {
	s := liveShare(t)
	base := testBase(t, s)
	tag := fmt.Sprintf("smbq%d", time.Now().UnixNano()%1e9)

	// Put a file in the path tree and tag it.
	if err := s.MkDir("/files"+base, 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}
	if err := s.WriteFile("/files"+base+"/q.txt", []byte("query bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dir, err := s.tree.PathDir(base)
	if err != nil || len(dir.Files) != 1 {
		t.Fatalf("PathDir: %v %+v", err, dir.Files)
	}
	hash := dir.Files[0].Uid
	if err := s.tree.TagsBinding(hash).Store([]string{tag}); err != nil {
		t.Fatalf("Store tags: %v", err)
	}

	qdir := "/query/" + tag
	if st := mustStat(t, s, qdir); st.Mode()&0xF000 != 0x4000 {
		t.Errorf("query dir mode = 0%o, want a directory", st.Mode())
	}
	if !contains(listNames(t, s, qdir), "q.txt") {
		t.Fatalf("query listing = %v, want q.txt", listNames(t, s, qdir))
	}
	qf := qdir + "/q.txt"
	if data, err := s.ReadFile(qf); err != nil || string(data) != "query bytes" {
		t.Fatalf("ReadFile(query) = %q, %v", data, err)
	}
	if st := mustStat(t, s, qf); st.Mode()&0o222 != 0 {
		t.Errorf("query result mode = 0%o, want read-only", st.Mode())
	}
	if err := s.WriteFile(qf, []byte("nope"), 0o644); !errors.Is(err, iofs.ErrPermission) {
		t.Errorf("WriteFile(query result) = %v, want fs.ErrPermission", err)
	}
	// A query that parses to nothing is not a directory.
	mustNotExist(t, s, "/query/-only")
}

func TestMetaTags(t *testing.T) {
	s := liveShare(t)
	base := testBase(t, s)
	if err := s.MkDir("/files"+base, 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}
	if err := s.WriteFile("/files"+base+"/tagme.txt", []byte("tag me"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dir, err := s.tree.PathDir(base)
	if err != nil || len(dir.Files) != 1 {
		t.Fatalf("PathDir: %v %+v", err, dir.Files)
	}
	hash := dir.Files[0].Uid

	// Whole-file write of a tag leaf replaces the tag set.
	leaf := "/tags" + base + "/tagme.txt"
	if err := s.WriteFile(leaf, []byte("jazz\nmellow\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tag leaf): %v", err)
	}
	vals, err := s.tree.TagsBinding(hash).Load()
	if err != nil || !contains(vals, "jazz") || !contains(vals, "mellow") {
		t.Fatalf("tags after write = %v, %v", vals, err)
	}
	if st := mustStat(t, s, leaf); st.Mode() != 0o100644 {
		t.Errorf("tag leaf mode = 0%o, want 0644", st.Mode())
	}

	// Open reads the current set.
	f, err := s.OpenFile(leaf)
	if err != nil {
		t.Fatalf("OpenFile(tag leaf): %v", err)
	}
	buf := make([]byte, f.Size())
	if n, err := f.ReadAt(buf, 0); n != len(buf) || (err != nil && !errors.Is(err, io.EOF)) {
		t.Fatalf("read tag leaf: %d %v", n, err)
	}
	f.Close()
	if !strings.Contains(string(buf), "jazz") {
		t.Errorf("tag leaf content = %q", buf)
	}

	// CREATE flow on a meta leaf: truncate to empty, stage, rewrite, commit.
	if err := s.WriteFile(leaf, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(leaf,nil): %v", err)
	}
	if st := mustStat(t, s, leaf); st.Size() != 0 {
		t.Errorf("pending meta Stat size = %d, want 0", st.Size())
	}
	f2, err := s.OpenFile(leaf)
	if err != nil {
		t.Fatalf("OpenFile(pending meta): %v", err)
	}
	if _, err := f2.(gofs.WritableFile).WriteAt([]byte("solo\n"), 0); err != nil {
		t.Fatalf("WriteAt(meta): %v", err)
	}
	if err := f2.Close(); err != nil {
		t.Fatalf("Close(meta): %v", err)
	}
	vals, err = s.tree.TagsBinding(hash).Load()
	if err != nil || len(vals) != 1 || vals[0] != "solo" {
		t.Fatalf("tags after rewrite = %v, %v", vals, err)
	}

	// The directory's own .tags and .type control files.
	uid, err := s.tree.Tie.DirUIDFromPath(base)
	if err != nil || uid == "" {
		t.Fatalf("DirUIDFromPath: %q %v", uid, err)
	}
	if err := s.WriteFile("/tags"+base+"/"+tiesfs.TagsSelfName, []byte("dirtag\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.tags): %v", err)
	}
	if vals, err := s.tree.TagsBinding(string(uid)).Load(); err != nil || !contains(vals, "dirtag") {
		t.Fatalf("dir tags = %v, %v", vals, err)
	}
	if err := s.WriteFile("/tags"+base+"/"+tiesfs.TagsTypeName, []byte("live-album\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.type): %v", err)
	}
	if vals, err := s.tree.DirTypesBinding(uid).Load(); err != nil || !contains(vals, "live-album") {
		t.Fatalf("dir types = %v, %v", vals, err)
	}
	names := listNames(t, s, "/tags"+base)
	for _, want := range []string{tiesfs.TagsSelfName, tiesfs.TagsTypeName, "tagme.txt"} {
		if !contains(names, want) {
			t.Errorf("ListDir(/tags%s) = %v, want %q", base, names, want)
		}
	}

	// A tag leaf for a file that is not in the tree does not exist.
	mustNotExist(t, s, "/tags"+base+"/nosuch.txt")
	if err := s.WriteFile("/tags"+base+"/nosuch.txt", []byte("x"), 0o644); !errors.Is(err, iofs.ErrNotExist) {
		t.Errorf("WriteFile(missing leaf) = %v, want fs.ErrNotExist", err)
	}
}

func TestEscapedNamesRoundTrip(t *testing.T) {
	s := liveShare(t)
	base := testBase(t, s)

	// Create raw tie names that SMB display rules forbid, through tiesfs
	// (bypassing the escape), then reach them over the escaped view.
	dirName := "we:ird*name"
	fileName := "a b:c?.txt"
	if _, err := s.tree.Mkdir(base, dirName); err != nil {
		t.Fatalf("tree.Mkdir: %v", err)
	}
	inner := tiesfs.ChildPath(base, dirName)
	wh, err := s.tree.NewWriteHandle(inner, fileName, client.File{})
	if err != nil {
		t.Fatalf("NewWriteHandle: %v", err)
	}
	wh.WriteAt([]byte("escaped bytes"), 0)
	if err := wh.Close(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	escDir := escapeName(dirName)
	escFile := escapeName(fileName)
	if strings.ContainsAny(escDir+escFile, `:*/\?"<>|`) {
		t.Fatalf("escaped names still illegal: %q %q", escDir, escFile)
	}
	if !contains(listNames(t, s, "/files"+base), escDir) {
		t.Fatalf("ListDir(/files%s) = %v, want %q", base, listNames(t, s, "/files"+base), escDir)
	}
	mustStat(t, s, "/files"+base+"/"+escDir)
	if !contains(listNames(t, s, "/files"+base+"/"+escDir), escFile) {
		t.Fatalf("escaped dir listing missing %q", escFile)
	}
	full := "/files" + base + "/" + escDir + "/" + escFile
	if data, err := s.ReadFile(full); err != nil || string(data) != "escaped bytes" {
		t.Fatalf("ReadFile(escaped) = %q, %v", data, err)
	}

	// Rename through escaped spellings.
	if err := s.Rename(full, "/files"+base+"/"+escDir+"/plain.txt"); err != nil {
		t.Fatalf("Rename(escaped): %v", err)
	}
	mustNotExist(t, s, full)
	if data, err := s.ReadFile("/files" + base + "/" + escDir + "/plain.txt"); err != nil || string(data) != "escaped bytes" {
		t.Fatalf("ReadFile(renamed) = %q, %v", data, err)
	}
}

func listNames(t *testing.T, s *Share, p string) []string {
	t.Helper()
	entries, err := s.ListDir(p)
	if err != nil {
		t.Fatalf("ListDir(%q): %v", p, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
