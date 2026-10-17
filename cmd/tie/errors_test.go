package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
)

// TestErrorPrefixNamesTheCommand: failures used to read `tie: Add Error: …` —
// which command, and why say "Error" twice? main now prefixes the command the
// operator ran (§6.8).
func TestErrorPrefixNamesTheCommand(t *testing.T) {
	root := newRootCommand(newTestApp(t))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tie"}, "tie:"},
		{[]string{"tie", "--help"}, "tie:"},
		{[]string{"tie", "get", "k1"}, "tie get:"},
		{[]string{"tie", "-C", "other.toml", "tag", "files", "jazz"}, "tie tag files:"},
		{[]string{"tie", "--format", "json", "del", "k1", "--all"}, "tie del:"},
		{[]string{"tie", "verify", "--check-blobs"}, "tie verify:"},
		{[]string{"tie", "repair", "plan"}, "tie repair plan:"},
		// a local flag is not a command, and a value is not either
		{[]string{"tie", "get", "-c", "Other", "k"}, "tie get:"},
		{[]string{"tie", "-c", "Other", "tag", "del", "jazz"}, "tie tag del:"},
	} {
		if got := errorPrefix(root, tc.args); got != tc.want {
			t.Errorf("%v -> %q, want %q", tc.args, got, tc.want)
		}
	}
}

// TestUsageErrorsArePlainAndExitTwo: a wrong argument list is a usage error (2),
// and its message is a sentence, not "Need 1 arg: tag" or "Tag add Error: …".
func TestUsageErrorsArePlainAndExitTwo(t *testing.T) {
	a := newTestApp(t)
	for _, args := range [][]string{
		{"tag", "files"},
		{"tag", "add"},
		{"tag", "rename", "one"},
		{"add", "k", "v"},
		{"upload"},
		{"versions", "list"},
	} {
		_, _, code, runErr := runTie(t, a, "", args...)
		if code != exitUsage {
			t.Errorf("`tie %s` exited %d, want %d", strings.Join(args, " "), code, exitUsage)
		}
		if runErr == nil {
			t.Errorf("`tie %s` reported no error", strings.Join(args, " "))
			continue
		}
		msg := runErr.Error()
		if strings.Contains(msg, "Error") {
			t.Errorf("`tie %s` still says Error twice: %q", strings.Join(args, " "), msg)
		}
		if r := msg[0]; r >= 'A' && r <= 'Z' {
			t.Errorf("`tie %s` message is capitalised: %q", strings.Join(args, " "), msg)
		}
	}
}

// TestOperationalErrorsArePlain: a server failure reads as what failed, in lower
// case, and exits 4 (§6.5, §6.8).
func TestOperationalErrorsArePlain(t *testing.T) {
	cfg := client.Config{
		TripleStoreURL: "http://127.0.0.1:1",
		Namespace:      "testing",
		Collection:     "testing",
	}
	a := &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}
	_, _, code, runErr := runTie(t, a, "", "verify")
	if code != exitOperational {
		t.Fatalf("verify against a dead store exited %d, want %d", code, exitOperational)
	}
	msg := runErr.Error()
	if strings.Contains(msg, "Error:") {
		t.Errorf("the failure still carries an Error: prefix: %q", msg)
	}
	if r := msg[0]; r >= 'A' && r <= 'Z' {
		t.Errorf("the failure message is capitalised: %q", msg)
	}
}

// TestProgressNeedsATerminal: the bar is for a person watching. On a pipe or in
// cron it is thousands of carriage-return frames, and -q means quiet (§6.8).
func TestProgressNeedsATerminal(t *testing.T) {
	if progressWanted(false, false) {
		t.Error("a bar was drawn on a non-terminal")
	}
	if progressWanted(true, true) {
		t.Error("a bar was drawn under -q")
	}
	if !progressWanted(false, true) {
		t.Error("no bar was drawn on a terminal")
	}
}

// TestUploadOnAPipeHasNoBarFrames: the bar used to render to stderr whatever
// stderr was, filling a cron log with frames.
func TestUploadOnAPipeHasNoBarFrames(t *testing.T) {
	a := newTestApp(t)
	path := filepath.Join(t.TempDir(), "up.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 256*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code, runErr := runTie(t, a, "", "upload", path)
	if code != 0 {
		t.Fatalf("upload exited %d: %v", code, runErr)
	}
	if strings.Contains(stderr, "\r") {
		t.Errorf("the progress bar rendered to a non-terminal stderr:\n%q", stderr)
	}
	if !strings.Contains(stdout, "up.bin") {
		t.Errorf("upload reported no result row:\n%s", stdout)
	}
}
