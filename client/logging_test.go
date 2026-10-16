package client

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLibraryWritesNothingToTheTerminals is the §6.1 rule as an executable:
// importing — including the paths that used to print ("tagging <hash>",
// "Importing: …", "reconcile: versioning …") — must leave stdout and stderr
// untouched when the client was not given a logger. An embedder (tie-fm) has
// nowhere to put that noise, and `tie import | cut -f1` would swallow it.
func TestLibraryWritesNothingToTheTerminals(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	dir := importFixtureTree(t)
	host := tc.Config.FileHosts["default"]
	if err := tc.ImportDir(dir, host, "audio-dir", nil, "/quiet", 0); err != nil {
		t.Fatal(err)
	}
	// A second import with one file gone exercises the reconcile path, which
	// used to write "reconcile: versioning superseded …" to stderr.
	if err := os.Remove(filepath.Join(dir, "loose.flac")); err != nil {
		t.Fatal(err)
	}

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	importErr := tc.ImportDir(dir, host, "audio-dir", nil, "/quiet", 0)
	os.Stdout, os.Stderr = savedOut, savedErr
	outW.Close()
	errW.Close()

	var stdout, stderr bytes.Buffer
	stdout.ReadFrom(outR)
	stderr.ReadFrom(errR)
	if importErr != nil {
		t.Fatal(importErr)
	}
	if stdout.Len() > 0 {
		t.Errorf("the import wrote %d bytes to stdout: %q", stdout.Len(), stdout.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("the import wrote %d bytes to stderr: %q", stderr.Len(), stderr.String())
	}
}

// TestLoggerReceivesWhatTheLibraryNotices: the same run, with a logger wired,
// reports what happened where an embedder can capture it.
func TestLoggerReceivesWhatTheLibraryNotices(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	dir := importFixtureTree(t)
	host := tc.Config.FileHosts["default"]
	if err := tc.ImportDir(dir, host, "audio-dir", nil, "/logged", 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "loose.flac")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tc.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := tc.ImportDir(dir, host, "audio-dir", nil, "/logged", 0); err != nil {
		t.Fatal(err)
	}

	logged := buf.String()
	for _, want := range []string{
		`versioning superseded child`,
		`filename=loose.flac`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the logger never said %q; it said:\n%s", want, logged)
		}
	}
}
