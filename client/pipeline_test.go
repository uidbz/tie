package client

import (
	"bytes"
	"strings"
	"testing"
)

// TestFindingsRoundTrip: `tie verify`'s rows are what `tie repair plan` reads
// (§6.5), so the classes and the nodes they name have to survive the trip —
// otherwise the pipeline repairs a different store than the one it scanned.
func TestFindingsRoundTrip(t *testing.T) {
	rep := &VerifyReport{
		OrphanDirs:  []DirUID{"dir-a", "dir-b"},
		OrphanFiles: []string{"file-1"},
		DanglingParentRefs: []ParentRef{
			{Child: "child-1", Parent: "gone-1"},
			{Child: "child-2", Parent: "gone-2"},
		},
		Cycles:         [][]DirUID{{"c1", "c2", "c1"}},
		DuplicatePaths: map[string][]DirUID{"/music": {"uid-1", "uid-2"}},
		MissingMetadata: []MetadataGap{
			{Subject: "hash-1", IsDir: false, Missing: []string{"filename", "media-type"}},
			{Subject: "dir-1", IsDir: true, Missing: []string{"name"}},
		},
		MissingBlobs: []string{"blob-1"},
	}

	findings := rep.Findings()
	if len(findings) != 2+1+2+1+1+2+1 {
		t.Fatalf("Findings() returned %d rows, want 10: %v", len(findings), findings)
	}

	back := ReportFromFindings(findings)

	if len(back.OrphanDirs) != 2 || back.OrphanDirs[0] != "dir-a" || back.OrphanDirs[1] != "dir-b" {
		t.Errorf("orphan dirs came back as %v", back.OrphanDirs)
	}
	if len(back.OrphanFiles) != 1 || back.OrphanFiles[0] != "file-1" {
		t.Errorf("orphan files came back as %v", back.OrphanFiles)
	}
	if len(back.DanglingParentRefs) != 2 ||
		back.DanglingParentRefs[0] != (ParentRef{Child: "child-1", Parent: "gone-1"}) ||
		back.DanglingParentRefs[1] != (ParentRef{Child: "child-2", Parent: "gone-2"}) {
		t.Errorf("dangling refs came back as %v", back.DanglingParentRefs)
	}
	if len(back.MissingMetadata) != 2 {
		t.Fatalf("metadata gaps came back as %v", back.MissingMetadata)
	}
	gap := back.MissingMetadata[0]
	if gap.Subject != "hash-1" || gap.IsDir || strings.Join(gap.Missing, ",") != "filename,media-type" {
		t.Errorf("file gap came back as %+v", gap)
	}
	dirGap := back.MissingMetadata[1]
	if dirGap.Subject != "dir-1" || !dirGap.IsDir || strings.Join(dirGap.Missing, ",") != "name" {
		t.Errorf("dir gap came back as %+v", dirGap)
	}
	if len(back.MissingBlobs) != 1 || back.MissingBlobs[0] != "blob-1" {
		t.Errorf("missing blobs came back as %v", back.MissingBlobs)
	}
	// Cycles and duplicate paths are reported, never planned: they survive as
	// findings for the human, but they must not become operations.
	if len(back.Cycles) != 0 || len(back.DuplicatePaths) != 0 {
		t.Errorf("unfixable classes were rebuilt for planning: %v %v", back.Cycles, back.DuplicatePaths)
	}
}

// TestParsePlanRow: a plan record and a journal record are the same shape, so a
// journal replays through `tie repair apply` (§6.5).
func TestParsePlanRow(t *testing.T) {
	op, err := ParsePlanRow([]string{"delete", "k1", "parent", "p1", "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != "delete" || op.Key != "k1" || op.Relation != "parent" || op.Value != "p1" || op.Note != "garbage" {
		t.Errorf("plan row parsed as %+v", op)
	}

	journal, err := ParsePlanRow([]string{"2026-10-17T00:00:00Z", "add", "k1", "tag", "jazz", "re-home"})
	if err != nil {
		t.Fatalf("a journal record did not parse: %v", err)
	}
	if journal.Kind != "add" || journal.Key != "k1" || journal.Value != "jazz" {
		t.Errorf("journal row parsed as %+v", journal)
	}

	for _, bad := range [][]string{
		{"delete", "k1", "parent", "p1"},
		{"update", "k1", "parent", "p1", "note"},
		{"delete", "", "parent", "p1", "note"},
		{"delete", "k1", "", "p1", "note"},
		{"delete", "k1", "parent", "p1", "note", "extra", "extra"},
	} {
		if _, err := ParsePlanRow(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// TestReadPlanNamesTheBadLine: the input is usually a file someone edited, so
// the error has to say which line is wrong.
func TestReadPlanNamesTheBadLine(t *testing.T) {
	plan := "add\tk1\ttag\tjazz\tre-home\n\nnot-a-record\n"
	if _, err := ReadPlan(strings.NewReader(plan)); err == nil {
		t.Fatal("a malformed plan was accepted")
	} else if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the error does not name the line: %v", err)
	}

	ops, err := ReadPlan(strings.NewReader("delete\tk1\tparent\tp1\tnote\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Kind != "delete" {
		t.Errorf("plan read as %v", ops)
	}
}

// TestReadFindingsRejectsShortRecords.
func TestReadFindingsRejectsShortRecords(t *testing.T) {
	if _, err := ReadFindings(strings.NewReader("orphan-dir\tuid-1\n")); err == nil {
		t.Error("a three-field finding was accepted")
	}
	findings, err := ReadFindings(strings.NewReader("missing-blob\thash-1\t\tnote\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != FindingMissingBlob || findings[0].Subject != "hash-1" {
		t.Errorf("findings read as %v", findings)
	}
}

// TestApplyPlanAppliesAndJournals: `tie repair apply` runs the operations a
// plan named, journals exactly what it applied, and the journal reads back.
func TestApplyPlanAppliesAndJournals(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	if err := tie.DropCollection(); err != nil {
		t.Fatal(err)
	}
	if _, err := tie.Add("hash-1", "filename", "old.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	var journal bytes.Buffer
	applied, err := tie.ApplyPlan([]RepairOp{
		{Kind: "delete", Key: "hash-1", Relation: "filename", Value: "old.txt", Note: "rename"},
		{Kind: "add", Key: "hash-1", Relation: "filename", Value: "new.txt", Note: "rename"},
	}, RepairOptions{Journal: &journal})
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("applied %d operations, want 2", applied)
	}

	row, err := tie.Get("hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if RowHas(row, "filename", "old.txt") || !RowHas(row, "filename", "new.txt") {
		t.Errorf("the plan did not take effect: %v", row.Attributes["filename"])
	}

	lines := strings.Split(strings.TrimSuffix(journal.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("journal has %d lines, want 2:\n%s", len(lines), journal.String())
	}
	for _, line := range lines {
		rec, err := SplitTSVRecord(line, 6)
		if err != nil {
			t.Fatalf("journal line does not read back: %v (%q)", err, line)
		}
		if rec[1] != "add" && rec[1] != "delete" {
			t.Errorf("journal op %q", rec[1])
		}
	}

	// The journal is a plan with a timestamp in front: replaying it is valid.
	replay, err := ReadPlan(strings.NewReader(journal.String()))
	if err != nil {
		t.Fatalf("the journal is not a readable plan: %v", err)
	}
	if len(replay) != 2 {
		t.Errorf("journal replayed as %d operations, want 2", len(replay))
	}
}

// TestApplyPlanAppliesNothingWhenTheBatchFails is the §5.1 L16 guard for the
// pipeline form: a journal must never claim operations that did not happen.
func TestApplyPlanAppliesNothingWhenTheBatchFails(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	if err := tie.DropCollection(); err != nil {
		t.Fatal(err)
	}
	if _, err := tie.Add("hash-1", "filename", "keep.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	cfg := tie.Config
	cfg.TripleStoreURL = batchFailingProxy(t).URL
	broken := NewTieClientFor(cfg, "")

	var journal bytes.Buffer
	applied, err := broken.ApplyPlan([]RepairOp{
		{Kind: "delete", Key: "hash-1", Relation: "filename", Value: "keep.txt", Note: "should not happen"},
	}, RepairOptions{Journal: &journal})
	if err == nil {
		t.Fatal("a refused Batch was reported as success")
	}
	if applied != 0 || journal.Len() != 0 {
		t.Errorf("applied %d, journal %q — nothing should have been recorded", applied, journal.String())
	}
}
