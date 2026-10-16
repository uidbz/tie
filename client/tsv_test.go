package client

import (
	"bytes"
	"strings"
	"testing"

	"github.com/uidbz/tie/tiedb"
)

// TestTSVRoundTrip: a field carrying the bytes that would break a line or a
// column survives dump → restore exactly, and the record on disk stays one line
// with exactly the right number of separators (§6.4).
func TestTSVRoundTrip(t *testing.T) {
	fields := []struct {
		key, value1, value2 string
	}{
		{"key\twith\ttabs", "tag", "value with\nnewline"},
		{"back\\slash", "tag", "quote\" and \r carriage"},
		{"", "tag", ""},
		{"plain", "tag", "plain"},
		{"trailing ", " tag ", " leading"},
	}

	var buf bytes.Buffer
	for _, f := range fields {
		line := TSVRecord(f.key, f.value1, f.value2)
		if strings.Contains(line, "\n") {
			t.Errorf("the record for %q spans more than one line: %q", f.key, line)
		}
		if got := strings.Count(line, "\t"); got != 2 {
			t.Errorf("the record for %q has %d separators, want 2: %q", f.key, got, line)
		}
		buf.WriteString(line + "\n")

		back, err := SplitTSVRecord(line, 3)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if back[0] != f.key || back[1] != f.value1 || back[2] != f.value2 {
			t.Errorf("%q read back as %q", line, back)
		}
	}
}

// TestSplitTSVRecordRejectsGarbage: a truncated escape or a wrong field count
// is an error, not a silently altered value.
func TestSplitTSVRecordRejectsGarbage(t *testing.T) {
	for _, line := range []string{`a\tb\`, `a\tb`, `a\tb\tc\qd`} {
		if _, err := SplitTSVRecord(line, 3); err == nil {
			t.Errorf("%q was accepted", line)
		}
	}
}

// TestSplitTSVRecordReadsOldQuotedDumps: dumps written before §6.4 quoted a
// field containing a tab. They still restore.
func TestSplitTSVRecordReadsOldQuotedDumps(t *testing.T) {
	line := "key\t\"value\twith\ttabs\"\tvalue2"
	got, err := SplitTSVRecord(line, 3)
	if err != nil {
		t.Fatalf("a pre-6.4 quoted dump was refused: %v", err)
	}
	if got[1] != "value\twith\ttabs" {
		t.Errorf("quoted field read as %q", got[1])
	}
}

// TestDumpRestoreRoundTrip is the end-to-end version: what `tie dump` writes,
// `tie restore` reads back byte for byte, through the server.
func TestDumpRestoreRoundTrip(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	want := [][3]string{
		{"awkward-key", "tag", "a\tb"},
		{"awkward-key", "note", "line1\nline2"},
		{"plain-key", "tag", "plain"},
	}
	for _, tr := range want {
		if _, err := tc.Add(tr[0], tr[1], tr[2]); err != nil {
			t.Fatal(err)
		}
	}

	var dump bytes.Buffer
	if err := tc.DumpStream(func(tr tiedb.StringTriple) error {
		dump.WriteString(TSVRecord(tr.Key, tr.Value1, tr.Value2) + "\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}
	n, err := tc.RestoreStream(bytes.NewReader(dump.Bytes()), nil)
	if err != nil {
		t.Fatalf("restoring our own dump failed: %v\n%s", err, dump.String())
	}
	if n != len(want) {
		t.Errorf("restore read %d triples, want %d", n, len(want))
	}

	for _, tr := range want {
		row, err := tc.Get(tr[0])
		if err != nil {
			t.Fatalf("%q/%q after restore: %v", tr[0], tr[1], err)
		}
		var found bool
		for _, v := range row.Attributes[tr[1]] {
			if v == tr[2] {
				found = true
			}
		}
		if !found {
			t.Errorf("%q/%q came back as %q, want %q", tr[0], tr[1], row.Attributes[tr[1]], tr[2])
		}
	}
}
