package metadata

import (
	"bytes"
	"testing"
)

func TestDirEntryRoundTrip(t *testing.T) {
	// Head deliberately contains a tab and a newline to prove encoding keeps
	// them from breaking column/line splitting.
	head := []byte{0x89, 'P', 'N', 'G', '\t', '\n', 0x00, 0xff}
	in := DirEntry{
		Hash:     "abc123",
		Filename: "some file.png",
		Size:     42,
		Head:     head,
	}

	line := in.Line()
	if line[len(line)-1] != '\n' {
		t.Fatal("line must end with newline")
	}

	out, ok := ParseDirLine(line[:len(line)-1])
	if !ok {
		t.Fatal("failed to parse line")
	}
	if out.Hash != in.Hash || out.Filename != in.Filename || out.Size != in.Size {
		t.Errorf("mismatch: got %+v want %+v", out, in)
	}
	if !bytes.Equal(out.Head, in.Head) {
		t.Errorf("head mismatch: got %v want %v", out.Head, in.Head)
	}
}

func TestParseDirLineRejectsMalformed(t *testing.T) {
	cases := []string{
		"only\ttwo",
		"a\tb\tnotanumber\t" + EncodeHead([]byte("x")),
		"a\tb\t5\t!!!not-base64!!!",
	}
	for _, c := range cases {
		if _, ok := ParseDirLine(c); ok {
			t.Errorf("expected parse failure for %q", c)
		}
	}
}

func TestIsDirHead(t *testing.T) {
	if !IsDirHead([]byte(DirHeader)) {
		t.Error("DirHeader should be detected as a directory")
	}
	if IsDirHead([]byte{0x89, 'P', 'N', 'G'}) {
		t.Error("PNG magic should not be a directory")
	}
}
