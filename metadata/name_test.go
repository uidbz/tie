package metadata

import (
	"strings"
	"testing"
)

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDirEntryNameRoundTrip is the §4.8 guard: Linux allows TAB and LF in a
// filename, and a raw one in a tiedir line either shifts the columns or splits
// the line, so every reader (download, mount, the reaper's dirChildren) drops
// the entry — and the reaper stops protecting a referenced child.
func TestDirEntryNameRoundTrip(t *testing.T) {
	names := []string{
		"plain.txt",
		"with\ttab",
		"with\nnewline",
		"with\rCR",
		"percent%sign",
		"looks like %09 escape",
		"100%.mp3",
		"tab\tand\nnewline",
		"emoji ✅.mp3",
	}
	for _, name := range names {
		e := DirEntry{Hash: testHash, Filename: name, Size: 7, Head: []byte("abc")}
		line := e.Line()
		got, ok := ParseDirLine(line)
		if !ok {
			t.Errorf("name %q: line %q does not parse", name, line)
			continue
		}
		if got.Filename != name {
			t.Errorf("name %q round-tripped as %q (line %q)", name, got.Filename, line)
		}
		// A manifest is line-oriented: the rendered line must hold exactly one
		// entry with exactly four columns, whatever the name contains.
		if strings.Count(line, "\n") != 1 {
			t.Errorf("name %q: rendered %d lines, want 1", name, strings.Count(line, "\n"))
		}
		if n := strings.Count(line, "\t"); n != 3 {
			t.Errorf("name %q: rendered line has %d tabs, want 3 (%q)", name, n, line)
		}
		if got.Hash != e.Hash || got.Size != e.Size {
			t.Errorf("name %q: other columns shifted: %+v", name, got)
		}
	}
}

// TestParseDirLineRejectsEscapedTraversal: decoding must not let a manifest
// smuggle a separator or a traversal name through the filename column.
func TestParseDirLineRejectsEscapedTraversal(t *testing.T) {
	for _, name := range []string{"%2E%2E", "..", "%2e%2e", "%2Fetc%2Fpasswd", "a/b"} {
		line := testHash + "\t" + name + "\t7\t" + EncodeHead([]byte("abc")) + "\n"
		if e, ok := ParseDirLine(line); ok {
			t.Errorf("manifest name %q was accepted as %q", name, e.Filename)
		}
	}
}
