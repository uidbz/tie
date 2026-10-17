package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"

	"github.com/urfave/cli/v3"
)

// TestPrintRowsIsDeterministic: the same query printed twice must be byte for
// byte identical. The relations of a row live in a map, so the old code put
// them in a different order on every run — which made `diff`, `comm` and golden
// tests useless (§6.4).
func TestPrintRowsIsDeterministic(t *testing.T) {
	rows := []client.Row{
		{Key: "zz", Attributes: map[string][]string{
			"zeta":   {"zv2", "zv1"},
			"alpha":  {"av"},
			"middle": {"mv"},
		}},
		{Key: "aa", Attributes: map[string][]string{"tag": {"t1", "t2"}}},
	}

	var first string
	for run := 0; run < 20; run++ {
		var buf bytes.Buffer
		o := newOut("tsv", &buf, "key", "value1", "value2")
		if err := printRows(o, rows); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = buf.String()
			continue
		}
		if buf.String() != first {
			t.Fatalf("run %d differs:\n%s---\n%s", run, first, buf.String())
		}
	}

	want := "aa\ttag\tt1\naa\ttag\tt2\nzz\talpha\tav\nzz\tmiddle\tmv\nzz\tzeta\tzv2\nzz\tzeta\tzv1\n"
	if first != want {
		t.Errorf("row order =\n%s\nwant\n%s", first, want)
	}
}

// TestOutputFormatSelection: an explicit --format wins, a bad one is a usage
// error, and with no flag a non-terminal gets TSV (§6.4).
func TestOutputFormatSelection(t *testing.T) {
	run := func(args ...string) (string, error) {
		var got string
		var gotErr error
		root := &cli.Command{
			Name:  "tie",
			Flags: []cli.Flag{&cli.StringFlag{Name: "format"}},
			Action: func(_ context.Context, c *cli.Command) error {
				got, gotErr = outputFormat(c)
				return nil
			},
		}
		if err := root.Run(context.Background(), append([]string{"tie"}, args...)); err != nil {
			return "", err
		}
		return got, gotErr
	}

	for _, want := range []string{"tsv", "json", "table"} {
		got, err := run("--format", want)
		if err != nil || got != want {
			t.Errorf("--format %s: got %q, err %v", want, got, err)
		}
	}

	if _, err := run("--format", "yaml"); err == nil {
		t.Error("--format yaml was accepted")
	}

	// The test binary's stdout is not a terminal, so the default is TSV —
	// which is what a pipe or a script wants.
	got, err := run()
	if err != nil || got != "tsv" {
		t.Errorf("default format = %q (err %v), want tsv off a terminal", got, err)
	}
}

// TestJSONRowsAreKeyedByColumn: --format json says what each column means, and
// a value containing a tab or a newline stays intact inside the JSON string.
func TestJSONRowsAreKeyedByColumn(t *testing.T) {
	var buf bytes.Buffer
	o := newOut("json", &buf, "hash", "filename")
	o.row("abc123", "two\tlines\nreally")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}

	var rows []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if len(rows) != 1 || rows[0]["hash"] != "abc123" || rows[0]["filename"] != "two\tlines\nreally" {
		t.Errorf("json rows = %v", rows)
	}
}

// TestTableCarriesAHeader: the table format is the human one, so it names its
// columns; TSV and JSON do not repeat a header on every record.
func TestTableCarriesAHeader(t *testing.T) {
	var buf bytes.Buffer
	o := newOut("table", &buf, "hash", "filename")
	o.row("abc123", "x.txt")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("table =\n%s", buf.String())
	}
	if !strings.Contains(lines[0], "HASH") || !strings.Contains(lines[0], "FILENAME") {
		t.Errorf("the table header = %q", lines[0])
	}

	var tsv bytes.Buffer
	o = newOut("tsv", &tsv, "hash", "filename")
	o.row("abc123", "x.txt")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tsv.String(), "HASH") {
		t.Errorf("tsv output carries a header: %q", tsv.String())
	}
}

// TestEscapedFieldsSurviveTheRenderer: a value with a tab in it must not shift
// the columns of a TSV row, and must read back unchanged.
func TestEscapedFieldsSurviveTheRenderer(t *testing.T) {
	var buf bytes.Buffer
	o := newOut("tsv", &buf, "key", "value1", "value2")
	o.row("k", "tag", "a\tb\nc")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(buf.String(), "\n")
	if strings.Count(line, "\t") != 2 {
		t.Errorf("the row has %d separators, want 2: %q", strings.Count(line, "\t"), line)
	}
	back, err := splitTSVLine(line, 3)
	if err != nil {
		t.Fatal(err)
	}
	if back[2] != "a\tb\nc" {
		t.Errorf("value read back as %q", back[2])
	}
}

// TestTableKeepsOneLinePerRecord: the table is the human format, but a value
// carrying a tab or a newline must not split its row or invent a column.
func TestTableKeepsOneLinePerRecord(t *testing.T) {
	var buf bytes.Buffer
	o := newOut("table", &buf, "key", "value1", "value2")
	o.row("k1", "note", "line1\nline2")
	o.row("k1", "tag", "a\tb")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("3 records printed as %d lines:\n%s", len(lines), buf.String())
	}
	// tabwriter turns the separators into padding, so what matters is that the
	// awkward bytes appear escaped and never raw.
	joined := buf.String()
	for _, want := range []string{`line1\nline2`, `a\tb`} {
		if !strings.Contains(joined, want) {
			t.Errorf("the table did not escape as %q:\n%s", want, joined)
		}
	}
}

// TestEmptyResultRendersNothing: a command with no rows must not print a header
// for an empty table (a multi-section report would repeat it), and its JSON is
// an empty array a consumer can iterate without a special case.
func TestEmptyResultRendersNothing(t *testing.T) {
	var table bytes.Buffer
	o := newOut("table", &table, "kind", "subject")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	if table.String() != "" {
		t.Errorf("an empty table printed %q, want nothing", table.String())
	}

	var js bytes.Buffer
	o = newOut("json", &js, "kind", "subject")
	if err := o.flush(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(js.String()) != "[]" {
		t.Errorf("an empty json result printed %q, want []", js.String())
	}
}
