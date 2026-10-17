package smbfs

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"

	smb "github.com/go-filesystems/smb"
	smb2 "github.com/hirochachacha/go-smb2"
)

// TestSMBEndToEnd runs a real SMB2 session against an in-process tie-smb
// server on a high port: mount the share, then list, read, create/write,
// rename, delete, and edit tags - the full round trip through the wire
// protocol. Skips when the test triplestore is down.
func TestSMBEndToEnd(t *testing.T) {
	sh := liveShare(t)

	srv := smb.New()
	srv.SetName("tietest")
	srv.AddUser("writer", "write-pass")
	srv.AddUser("reader", "read-pass")
	if err := srv.Share("tie", sh, smb.WriteUsers("writer")); err != nil {
		t.Fatalf("Share: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	mount := func(t *testing.T, user, pass string) *smb2.Share {
		t.Helper()
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: pass}}
		sess, err := d.Dial(conn)
		if err != nil {
			t.Fatalf("smb dial/auth: %v", err)
		}
		t.Cleanup(func() { sess.Logoff() })
		fs, err := sess.Mount("tie")
		if err != nil {
			t.Fatalf("mount tie: %v", err)
		}
		t.Cleanup(func() { fs.Umount() })
		return fs
	}

	fs := mount(t, "writer", "write-pass")
	stamp := time.Now().UnixNano()
	dir := fmt.Sprintf("e2e-%d", stamp)

	// Top level and the README.
	if names, err := fs.ReadDir("."); err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	} else if len(names) != 3 {
		t.Errorf("root has %d entries, want 3 (query/files/tags)", len(names))
	}
	if data, err := fs.ReadFile("query/README"); err != nil || len(data) == 0 {
		t.Errorf("ReadFile(query/README) = %d bytes, %v", len(data), err)
	}

	// mkdir, create+write+close, read back.
	if err := fs.Mkdir("files/"+dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	file := "files/" + dir + "/song.txt"
	f, err := fs.Create(file)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("smb bytes")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if data, err := fs.ReadFile(file); err != nil || string(data) != "smb bytes" {
		t.Fatalf("ReadFile = %q, %v; want %q", data, err, "smb bytes")
	}
	if names, err := fs.ReadDir("files/" + dir); err != nil || len(names) != 1 || names[0].Name() != "song.txt" {
		t.Errorf("ReadDir = %v, %v; want [song.txt]", names, err)
	}

	// In-place partial edit: open read-write, overwrite a prefix.
	f, err = fs.OpenFile(file, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(rw): %v", err)
	}
	if _, err := f.WriteAt([]byte("SMB"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(edit): %v", err)
	}
	if data, err := fs.ReadFile(file); err != nil || string(data) != "SMB bytes" {
		t.Errorf("after edit ReadFile = %q, %v; want %q", data, err, "SMB bytes")
	}

	// Rename (go-smb2 sends FileRenameInformation).
	if err := fs.Rename(file, "files/"+dir+"/song2.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := fs.Stat(file); err == nil {
		t.Error("old name still present after rename")
	}
	if data, err := fs.ReadFile("files/" + dir + "/song2.txt"); err != nil || string(data) != "SMB bytes" {
		t.Errorf("after rename ReadFile = %q, %v", data, err)
	}

	// Tag editing through tags/<dir>/<file>.
	tag := fmt.Sprintf("smbtag%d", stamp)
	meta := "tags/" + dir + "/song2.txt"
	if err := fs.WriteFile(meta, []byte(tag+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tags): %v", err)
	}
	if data, err := fs.ReadFile(meta); err != nil || string(data) != tag+"\n" {
		t.Errorf("ReadFile(tags) = %q, %v", data, err)
	}
	// The query view sees the tagged file.
	names, err := fs.ReadDir("query/" + tag)
	if err != nil || len(names) != 1 || names[0].Name() != "song2.txt" {
		t.Errorf("ReadDir(query/%s) = %v, %v; want [song2.txt]", tag, names, err)
	}

	// Delete file, then the empty directory.
	if err := fs.Remove("files/" + dir + "/song2.txt"); err != nil {
		t.Fatalf("Remove(file): %v", err)
	}
	if err := fs.Remove("files/" + dir); err != nil {
		t.Errorf("Remove(dir): %v", err)
	}

	// Read-only user: writes refused, reads fine.
	ro := mount(t, "reader", "read-pass")
	if data, err := ro.ReadFile("query/README"); err != nil || len(data) == 0 {
		t.Errorf("reader ReadFile README = %d bytes, %v", len(data), err)
	}
	if _, err := ro.Create("files/should-fail.txt"); err == nil {
		t.Error("reader Create succeeded, want MEDIA_WRITE_PROTECTED")
	}
}

// TestSMBNameEscapingOverTheWire creates tie names that SMB display rules
// forbid (through tiefs, bypassing the escape), then verifies a real SMB
// client sees the escaped spellings and can open them: the escape must
// round-trip through the transport, not just in-process.
func TestSMBNameEscapingOverTheWire(t *testing.T) {
	sh := liveShare(t)
	base := testBase(t, sh)

	raw := "we:ird*name"
	if _, err := sh.tree.Mkdir(base, raw); err != nil {
		t.Fatalf("tree.Mkdir: %v", err)
	}
	wh, err := sh.tree.NewWriteHandle(tiefs.ChildPath(base, raw), "trail.", client.File{})
	if err != nil {
		t.Fatalf("NewWriteHandle: %v", err)
	}
	if _, err := wh.WriteAt([]byte("wire"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := wh.Close(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	srv := smb.New()
	srv.AddUser("writer", "write-pass")
	if err := srv.Share("tie", sh, smb.WriteUsers("writer")); err != nil {
		t.Fatalf("Share: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: "writer", Password: "write-pass"}}
	sess, err := d.Dial(conn)
	if err != nil {
		t.Fatalf("smb dial/auth: %v", err)
	}
	defer sess.Logoff()
	fs, err := sess.Mount("tie")
	if err != nil {
		t.Fatalf("mount tie: %v", err)
	}
	defer fs.Umount()

	escDir := escapeName(raw)
	escFile := escapeName("trail.")
	names, err := fs.ReadDir("files" + base)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if !contains(dirNameList(names), escDir) {
		t.Fatalf("escaped dir %q missing from listing %v", escDir, dirNameList(names))
	}
	if names, err = fs.ReadDir("files" + base + "/" + escDir); err != nil {
		t.Fatalf("ReadDir(escaped dir): %v", err)
	} else if !contains(dirNameList(names), escFile) {
		t.Fatalf("escaped file %q missing from listing %v", escFile, dirNameList(names))
	}
	data, err := fs.ReadFile(fmt.Sprintf("files%s/%s/%s", base, escDir, escFile))
	if err != nil || string(data) != "wire" {
		t.Fatalf("ReadFile(escaped) = %q, %v", data, err)
	}
}

// dirNameList extracts names from a go-smb2 ReadDir result.
func dirNameList(infos []os.FileInfo) []string {
	names := make([]string, 0, len(infos))
	for _, fi := range infos {
		names = append(names, fi.Name())
	}
	return names
}
