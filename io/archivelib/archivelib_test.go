package archivelib

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

// tiny valid file headers for magic-byte sniffing (github.com/h2non/filetype).
var (
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	pngBytes  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	mp3Bytes  = []byte{'I', 'D', '3', 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	txtBytes  = []byte("just some plain text, not a recognized media type\n")
)

func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func TestClassifyImageArchive(t *testing.T) {
	z := buildZip(t, map[string][]byte{
		"a.jpg": jpegBytes,
		"b.png": pngBytes,
	})
	members, err := List(bytes.NewReader(z))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	if k := ModalKind(members); k != Image {
		t.Fatalf("ModalKind = %v, want Image", k)
	}
}

func TestClassifyAudioArchiveWithCover(t *testing.T) {
	z := buildZip(t, map[string][]byte{
		"01.mp3":    mp3Bytes,
		"02.mp3":    mp3Bytes,
		"cover.jpg": jpegBytes,
	})
	members, err := List(bytes.NewReader(z))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("got %d members, want 3 (cover listed as a member)", len(members))
	}
	if k := ModalKind(members); k != Audio {
		t.Fatalf("ModalKind = %v, want Audio (tracks dominate the cover)", k)
	}
}

func TestClassifyMixedFallsBackToUnknown(t *testing.T) {
	z := buildZip(t, map[string][]byte{
		"notes.txt": txtBytes,
		"more.txt":  txtBytes,
	})
	members, err := List(bytes.NewReader(z))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if k := ModalKind(members); k != Unknown {
		t.Fatalf("ModalKind = %v, want Unknown for an archive of unrecognized files", k)
	}
}

func TestOpenMember(t *testing.T) {
	z := buildZip(t, map[string][]byte{"a.jpg": jpegBytes})
	rc, err := Open(bytes.NewReader(z), "a.jpg")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, jpegBytes) {
		t.Fatalf("member bytes mismatch: got %v", got)
	}
}

func TestOpenMissingMember(t *testing.T) {
	z := buildZip(t, map[string][]byte{"a.jpg": jpegBytes})
	if _, err := Open(bytes.NewReader(z), "missing.jpg"); err == nil {
		t.Fatal("Open of missing member: want error, got nil")
	}
}
