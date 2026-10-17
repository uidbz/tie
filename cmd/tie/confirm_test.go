package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr redirected, so a test can check what the
// user is told (the prompt) apart from what the command prints.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	runErr := fn()
	w.Close()
	os.Stderr = saved
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String(), runErr
}

// fakeTerminal hands confirmAlbumImportOn a file to read the answer from, in
// place of /dev/tty.
func fakeTerminal(t *testing.T, answer string) func() (*os.File, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() (*os.File, error) { return os.Open(path) }
}

// TestConfirmRefusesWithoutATerminal: with no controlling terminal — cron, a
// pipe, `nohup … &` — the album prompt must not silently abort the import, and
// must not be answered by whatever happens to be on stdin. It refuses, exit 2,
// and says how to confirm non-interactively (§6.3).
func TestConfirmRefusesWithoutATerminal(t *testing.T) {
	yes, err := confirmAlbumImportOn(3, func() (*os.File, error) {
		return nil, errors.New("open /dev/tty: no such device or address")
	})
	if yes {
		t.Error("the import was confirmed without a terminal")
	}
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v, want an exit-2 refusal", err)
	}
	if !strings.Contains(err.Error(), "-y") {
		t.Errorf("the refusal does not say how to confirm: %q", err)
	}
	if !strings.Contains(err.Error(), "3 albums") {
		t.Errorf("the refusal does not say what is being confirmed: %q", err)
	}
}

// TestConfirmReadsTheTerminal: the answer comes from the terminal, only y/yes
// confirms, and an EOF is a refusal rather than a crash.
func TestConfirmReadsTheTerminal(t *testing.T) {
	for _, c := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{"  Y \n", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
		{"", false}, // Ctrl-D
	} {
		yes, err := confirmAlbumImportOn(1, fakeTerminal(t, c.answer))
		if err != nil {
			t.Errorf("answer %q: %v", c.answer, err)
		}
		if yes != c.want {
			t.Errorf("answer %q confirmed = %v, want %v", c.answer, yes, c.want)
		}
	}
}

// TestConfirmPromptsOnStderr: the question goes where the user sees it without
// landing in the command's data (§6.2), and it names what is being confirmed.
func TestConfirmPromptsOnStderr(t *testing.T) {
	stderr, err := captureStderr(t, func() error {
		_, e := confirmAlbumImportOn(12, fakeTerminal(t, "n\n"))
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Import 12 albums? [y/N]") {
		t.Errorf("stderr = %q, want the prompt", stderr)
	}

	stdout, err := captureStdout(t, func() error {
		_, e := confirmAlbumImportOn(1, fakeTerminal(t, "y\n"))
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("the prompt wrote %q to stdout, want nothing", stdout)
	}
}
