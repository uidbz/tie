package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngMagic is the 8-byte PNG signature — far shorter than the 261-byte sniff
// window, so it exercises the short-read path.
var pngMagic = "\x89PNG\r\n\x1a\n"

func TestGetTieTypeEmptyReader(t *testing.T) {
	// A zero-byte file used to surface as a bare io.EOF and abort an import.
	got, err := GetTieType(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty reader: unexpected error %v", err)
	}
	if got != TieUnknownFile {
		t.Errorf("empty reader: got %v, want %v", got, TieUnknownFile)
	}
}

func TestGetTieTypeShortReader(t *testing.T) {
	got, err := GetTieType(strings.NewReader(pngMagic))
	if err != nil {
		t.Fatalf("short reader: unexpected error %v", err)
	}
	if got != TieImageFile {
		t.Errorf("short reader: got %v, want %v", got, TieImageFile)
	}
}

func TestGetTieTypeFromPathEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := GetTieTypeFromPath(path)
	if err != nil {
		t.Fatalf("empty file: unexpected error %v", err)
	}
	if got != TieUnknownFile {
		t.Errorf("empty file: got %v, want %v", got, TieUnknownFile)
	}
}

func TestGetTieTypeFromPathMissing(t *testing.T) {
	if _, err := GetTieTypeFromPath(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing file: expected an error")
	}
}
