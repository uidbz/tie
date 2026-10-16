package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/testenv"

	"github.com/urfave/cli/v3"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written. §6.2 is a claim about stdout, so a test has to look at it.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = saved
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String(), runErr
}

func runVersion(t *testing.T, a *app) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		root := &cli.Command{Name: "tie", Commands: []*cli.Command{cmdVersion(a)}}
		return root.Run(context.Background(), []string{"tie", "version"})
	})
}

// TestVersionRowsAreData: `tie version` answers one row per component —
// component, version, URL — so `tie version | cut -f2` works (§6.2).
func TestVersionRowsAreData(t *testing.T) {
	env, stop, err := testenv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	cfg := client.Config{
		TripleStoreURL: env.TripleStoreURL,
		Namespace:      "Collections",
		Collection:     "Main",
	}
	a := &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}

	out, err := runVersion(t, a)
	if err != nil {
		t.Fatalf("a reachable triplestore must not fail the command: %v\n%s", err, out)
	}

	rows := strings.Split(strings.TrimSpace(out), "\n")
	if len(rows) != 2 {
		t.Fatalf("version printed %d rows, want 2 (client, triplestore):\n%s", len(rows), out)
	}
	want := []string{"client", "triplestore"}
	for i, row := range rows {
		fields := strings.Split(row, "\t")
		if len(fields) != 3 {
			t.Fatalf("row %d = %q, want 3 tab-separated fields", i, row)
		}
		if fields[0] != want[i] {
			t.Errorf("row %d names %q, want %q", i, fields[0], want[i])
		}
		if fields[1] == "" || fields[1] == "unavailable" {
			t.Errorf("row %d has no version: %q", i, row)
		}
	}
	if !strings.Contains(rows[1], env.TripleStoreURL) {
		t.Errorf("the triplestore row does not name its URL: %q", rows[1])
	}
}

// TestVersionFailsWhenAServerIsUnreachable is what makes `tie version` usable
// as a health check: the row still appears (with `unavailable`), the reason
// goes to stderr, and the command exits non-zero (§6.2).
func TestVersionFailsWhenAServerIsUnreachable(t *testing.T) {
	cfg := client.Config{
		TripleStoreURL: "http://127.0.0.1:1",
		Namespace:      "Collections",
		Collection:     "Main",
	}
	a := &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}

	out, err := runVersion(t, a)
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("an unreachable server must exit 1, got %v", err)
	}

	rows := strings.Split(strings.TrimSpace(out), "\n")
	if len(rows) != 2 {
		t.Fatalf("version printed %d rows, want 2:\n%s", len(rows), out)
	}
	if !strings.HasPrefix(rows[1], "triplestore\tunavailable\t") {
		t.Errorf("the unreachable component row = %q, want triplestore/unavailable/URL", rows[1])
	}
}

// TestSummaryRowsAreTSV pins the shape the media commands print: the old
// `members=… added=…` form mixed labels into the data.
func TestSummaryRowsAreTSV(t *testing.T) {
	out, err := captureStdout(t, func() error {
		indexResultRow("tie:/music/album", client.IndexResult{
			Members: 12, AddedMembers: 3, DroppedStale: 1, Discs: 2, Ops: 40,
		})
		countRow("rescanned-tracks", 7)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "tie:/music/album\t12\t3\t1\t2\t40\nrescanned-tracks\t7\n"
	if out != want {
		t.Errorf("summary rows =\n%q\nwant\n%q", out, want)
	}
}
