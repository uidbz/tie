package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/tiedb"
)

// awkwardVerifyReport is a store with one finding of every class, including
// several duplicate-path claims — which live in a map, the iteration order that
// used to make two runs of `tie verify` disagree.
func awkwardVerifyReport() *client.VerifyReport {
	return &client.VerifyReport{
		OrphanDirs:  []client.DirUID{"dir-b", "dir-a"},
		OrphanFiles: []string{"file-1"},
		DanglingParentRefs: []client.ParentRef{
			{Child: "child-1", Parent: "gone-1"},
		},
		Cycles: [][]client.DirUID{{"c1", "c2", "c1"}},
		DuplicatePaths: map[string][]client.DirUID{
			"/music":  {"uid-music-2", "uid-music-1"},
			"/books":  {"uid-books"},
			"/photos": {"uid-p2", "uid-p1", "uid-p0"},
		},
		MissingMetadata: []client.MetadataGap{
			{Subject: "hash-1", IsDir: false, Missing: []string{"filename", "media-type"}},
			{Subject: "dir-1", IsDir: true, Missing: []string{"name"}},
		},
		MissingBlobs: []string{"blob-1"},
	}
}

// TestVerifyReportRowsAreShaped: every finding is one four-field record — kind,
// subject, and two detail columns — so `cut`, `sort` and `diff` work on the
// report, which is the command's product (§8 S12).
func TestVerifyReportRowsAreShaped(t *testing.T) {
	rows := verifyReportRows(awkwardVerifyReport())
	if len(rows) == 0 {
		t.Fatal("a report with findings produced no rows")
	}
	for _, row := range rows {
		if len(row) != 4 {
			t.Fatalf("row %q has %d fields, want 4", row, len(row))
		}
		if row[0] == "" {
			t.Errorf("row %q has no kind", row)
		}
	}

	kinds := map[string]bool{}
	for _, row := range rows {
		kinds[row[0]] = true
	}
	for _, want := range []string{
		"orphan-dir", "orphan-file", "dangling-parent", "cycle",
		"duplicate-path", "missing-metadata", "missing-blob",
	} {
		if !kinds[want] {
			t.Errorf("no row of kind %q; kinds seen: %v", want, kinds)
		}
	}
}

// TestVerifyReportRowsAreStable: the duplicate-path claims live in a map, so
// the old report printed them in a different order on every run — the same
// store could not be diffed against itself (§6.4/§8 S12).
func TestVerifyReportRowsAreStable(t *testing.T) {
	var first string
	for run := 0; run < 20; run++ {
		var buf bytes.Buffer
		o := newOut("tsv", &buf, "kind", "subject", "value1", "value2")
		printVerifyReport(o, awkwardVerifyReport())
		if err := o.flush(); err != nil {
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

	lines := strings.Split(strings.TrimSuffix(first, "\n"), "\n")
	if !sortedByField(lines) {
		t.Errorf("the report is not in a fixed order:\n%s", first)
	}
}

// sortedByField checks that TSV lines are ordered by their first field, then
// the rest — the order a consumer would get from `sort` anyway.
func sortedByField(lines []string) bool {
	for i := 1; i < len(lines); i++ {
		a, b := strings.Split(lines[i-1], "\t"), strings.Split(lines[i], "\t")
		less := false
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				less = a[k] < b[k]
				break
			}
		}
		if !less {
			return false
		}
	}
	return true
}

// TestIndexReportRows: the index cross-check answers the same four columns — a
// count row per class, then the samples — so one `tie verify` stream has one
// shape instead of a prose header plus indented alignment.
func TestIndexReportRows(t *testing.T) {
	rep := &tiedb.IndexReport{
		MissingReverse: 2,
		ReverseOnly:    1,
		Samples: []tiedb.IndexProblem{
			{Kind: tiedb.IndexReverseOnly, Key: "k1", Value1: "tag", Value2: "v"},
			{Kind: tiedb.IndexMissingReverse, Key: "k2", Value1: "tag", Value2: "v"},
		},
	}
	rows := indexReportRows(rep)

	var counts, samples int
	for _, row := range rows {
		if len(row) != 4 {
			t.Fatalf("row %q has %d fields, want 4", row, len(row))
		}
		if row[1] == "(count)" {
			counts++
			if row[2] == "" {
				t.Errorf("count row %q carries no count", row)
			}
			continue
		}
		samples++
	}
	if counts != 2 {
		t.Errorf("got %d count rows, want 2 (missing-reverse, reverse-only)", counts)
	}
	if samples != 2 {
		t.Errorf("got %d sample rows, want 2", samples)
	}

	clean := indexReportRows(&tiedb.IndexReport{ForwardEntries: 10, ReverseEntries: 10})
	if len(clean) != 0 {
		t.Errorf("a consistent index printed %d rows: %v", len(clean), clean)
	}
}
