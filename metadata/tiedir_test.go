package metadata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validHash is a well-formed 64-char lowercase hex content address for tests.
const validHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDirEntryRoundTrip(t *testing.T) {
	// Head deliberately contains a tab and a newline to prove encoding keeps
	// them from breaking column/line splitting.
	head := []byte{0x89, 'P', 'N', 'G', '\t', '\n', 0x00, 0xff}
	in := DirEntry{
		Hash:     validHash,
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
	head := EncodeHead([]byte("x"))
	h := validHash
	cases := []string{
		"only\ttwo",
		h + "\tb\tnotanumber\t" + head,
		h + "\tb\t5\t!!!not-base64!!!",
		// Filename must be a single component: reject traversal attempts.
		h + "\t..\t5\t" + head,
		h + "\t.\t5\t" + head,
		h + "\t\t5\t" + head,
		h + "\t../etc/passwd\t5\t" + head,
		h + "\tsub/child\t5\t" + head,
		h + "\t/abs\t5\t" + head,
		h + "\tsub\\child\t5\t" + head,
		// Hash must be exactly 64 lowercase hex chars: reject SSRF/path escape.
		"abc123\tb\t5\t" + head,
		"../../admin\tb\t5\t" + head,
		strings.ToUpper(h) + "\tb\t5\t" + head,
		h + "beef\tb\t5\t" + head,
		h[:63] + "\tb\t5\t" + head,
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

// TestContentAddressIsStable pins the address space. The key that defines it
// used to exist four times (client, metadata, the filehost, and a copy in
// Config); one of them drifting would silently fork the store — the same bytes
// would have two names, and uploads would not find their blobs.
func TestContentAddressIsStable(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"hello", "c738b072be8b8484d62c7399de2db2c0707b31ae65f4a325dfd6d7f690453cb5"},
		{DirHeader + "x", "a828b7370654c9b22d2319b63de039f0da7d8079ce8d8a7322012a12c704c7ed"},
	} {
		got, err := HashReader(strings.NewReader(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("content address of %q changed: got %s, want %s", tc.input, got, tc.want)
		}
	}
}

// TestHashKeyIsDefinedOnce is the guard against re-forking: the key literal may
// appear in exactly one file in this module.
func TestHashKeyIsDefinedOnce(t *testing.T) {
	const key = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"

	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}

	var found []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // this test names the key on purpose
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), key) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(found) != 1 {
		t.Errorf("the content-address key is defined in %d files (%v); it must live in metadata/tiedir.go alone", len(found), found)
	}
	if len(found) == 1 && filepath.Base(found[0]) != "tiedir.go" {
		t.Errorf("the content-address key lives in %s, not metadata/tiedir.go", found[0])
	}
}

// moduleRoot walks up from the working directory to the module root, so the walk
// covers the whole repo rather than this package.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
