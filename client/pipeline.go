package client

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/uidbz/tie/api"
)

// The fsck pipeline (§6.5): `tie verify` answers findings, `tie repair plan`
// turns findings into operations, `tie repair apply` runs the operations and
// echoes the journal. Each stage's stdout is the next stage's stdin, so the
// formats here are wire formats, not display details:
//
//	finding:  kind  subject  value1  value2
//	plan:     op    key      relation value  note
//	journal:  time  op       key      relation value note     (a plan record
//	                                                            with a time in
//	                                                            front)
//
// The plan and the journal are deliberately the same record, so a journal a
// repair wrote can be reviewed with cut/awk and replayed with `tie repair
// apply`.

// Finding kinds reported by Verify. The index classes (tiedb.IndexMissingReverse
// and friends) name server-side index divergences; those are fixed by
// CheckIndex's repair, not by a triple plan, so ReportFromFindings skips them.
const (
	FindingOrphanDir      = "orphan-dir"
	FindingOrphanFile     = "orphan-file"
	FindingDanglingParent = "dangling-parent"
	FindingCycle          = "cycle"
	FindingDuplicatePath  = "duplicate-path"
	FindingMissingMeta    = "missing-metadata"
	FindingMissingBlob    = "missing-blob"
)

// Finding is one problem Verify found: the class, the node it was found on, and
// up to two detail values.
type Finding struct {
	Kind    string
	Subject string
	Value1  string
	Value2  string
}

// Findings flattens the report's sections into findings. VerifyReport is the
// in-memory shape; Finding is what leaves the process, so the report a human
// reads and the report `tie repair plan` consumes cannot drift apart.
func (rep *VerifyReport) Findings() []Finding {
	if rep == nil {
		return nil
	}
	var out []Finding
	for _, u := range rep.OrphanDirs {
		out = append(out, Finding{FindingOrphanDir, string(u), "", ""})
	}
	for _, h := range rep.OrphanFiles {
		out = append(out, Finding{FindingOrphanFile, h, "", ""})
	}
	for _, e := range rep.DanglingParentRefs {
		out = append(out, Finding{FindingDanglingParent, e.Child, e.Parent, ""})
	}
	for _, c := range rep.Cycles {
		parts := make([]string, len(c))
		for i, u := range c {
			parts[i] = string(u)
		}
		subject := ""
		if len(parts) > 0 {
			subject = parts[0]
		}
		out = append(out, Finding{FindingCycle, subject, strings.Join(parts, " -> "), ""})
	}
	for p, uids := range rep.DuplicatePaths {
		parts := make([]string, len(uids))
		for i, u := range uids {
			parts[i] = string(u)
		}
		out = append(out, Finding{FindingDuplicatePath, p, strings.Join(parts, ", "), ""})
	}
	for _, g := range rep.MissingMetadata {
		kind := "file"
		if g.IsDir {
			kind = "dir"
		}
		out = append(out, Finding{FindingMissingMeta, g.Subject, kind, strings.Join(g.Missing, ", ")})
	}
	for _, h := range rep.MissingBlobs {
		out = append(out, Finding{FindingMissingBlob, h, "", ""})
	}
	return out
}

// ParseFinding reads one finding record back.
func ParseFinding(fields []string) (Finding, error) {
	if len(fields) != 4 {
		return Finding{}, fmt.Errorf("finding has %d fields, want 4", len(fields))
	}
	return Finding{Kind: fields[0], Subject: fields[1], Value1: fields[2], Value2: fields[3]}, nil
}

// ReportFromFindings rebuilds the report RepairTree plans from, out of findings
// read back from `tie verify`'s output (§6.5). The planner re-reads the store
// for everything it decides, so the findings only have to name the classes and
// the nodes.
//
// Index findings are dropped: they describe the server's association indexes,
// which `tie verify --index --repair` fixes, not a triple plan. Cycles and
// duplicate paths are kept for completeness but never planned — RepairTree
// reports them and refuses to guess.
func ReportFromFindings(findings []Finding) *VerifyReport {
	rep := &VerifyReport{}
	for _, f := range findings {
		switch f.Kind {
		case FindingOrphanDir:
			rep.OrphanDirs = append(rep.OrphanDirs, DirUID(f.Subject))
		case FindingOrphanFile:
			rep.OrphanFiles = append(rep.OrphanFiles, f.Subject)
		case FindingDanglingParent:
			rep.DanglingParentRefs = append(rep.DanglingParentRefs, ParentRef{Child: f.Subject, Parent: f.Value1})
		case FindingMissingMeta:
			var missing []string
			if f.Value2 != "" {
				missing = strings.Split(f.Value2, ", ")
			}
			rep.MissingMetadata = append(rep.MissingMetadata, MetadataGap{
				Subject: f.Subject, IsDir: f.Value1 == "dir", Missing: missing,
			})
		case FindingMissingBlob:
			rep.MissingBlobs = append(rep.MissingBlobs, f.Subject)
		}
	}
	return rep
}

// ParsePlanRow reads one plan or journal record: op, key, relation, value,
// note — optionally preceded by the timestamp a journal carries, which is what
// makes a journal replayable through the same command (§6.5).
func ParsePlanRow(fields []string) (RepairOp, error) {
	if len(fields) == 6 {
		fields = fields[1:] // the journal's timestamp
	}
	if len(fields) != 5 {
		return RepairOp{}, fmt.Errorf("plan record has %d fields, want 5: op, key, relation, value, note", len(fields))
	}
	op := RepairOp{
		Kind:     fields[0],
		Key:      fields[1],
		Relation: fields[2],
		Value:    fields[3],
		Note:     fields[4],
	}
	if op.Kind != api.BatchAdd && op.Kind != api.BatchDelete {
		return RepairOp{}, fmt.Errorf("unknown operation %q: want add or delete", op.Kind)
	}
	if op.Key == "" || op.Relation == "" {
		return RepairOp{}, fmt.Errorf("%s record names no key or relation", op.Kind)
	}
	return op, nil
}

// readTSVRecords reads records of want fields, one per line, skipping blank
// lines. A malformed record is reported with its line number, because the input
// is usually a file someone edited by hand.
func readTSVRecords(r io.Reader, want ...int) ([][]string, error) {
	br := bufio.NewReader(r)
	var out [][]string
	for n := 0; ; {
		line, err := br.ReadString('\n')
		if line != "" {
			n++
			text := strings.TrimRight(line, "\r\n")
			if text == "" {
				if err != nil && err != io.EOF {
					return out, err
				}
				continue
			}
			var fields []string
			var perr error
			for _, w := range want {
				if fields, perr = SplitTSVRecord(text, w); perr == nil {
					break
				}
			}
			if perr != nil {
				return out, fmt.Errorf("line %d: %w", n, perr)
			}
			out = append(out, fields)
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

// ReadFindings reads finding records from r (§6.5).
func ReadFindings(r io.Reader) ([]Finding, error) {
	records, err := readTSVRecords(r, 4)
	if err != nil {
		return nil, err
	}
	out := make([]Finding, 0, len(records))
	for i, rec := range records {
		f, perr := ParseFinding(rec)
		if perr != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, perr)
		}
		out = append(out, f)
	}
	return out, nil
}

// ReadPlan reads plan or journal records from r, accepting either the five
// columns of a plan or the six of a journal (§6.5).
func ReadPlan(r io.Reader) ([]RepairOp, error) {
	records, err := readTSVRecords(r, 5, 6)
	if err != nil {
		return nil, err
	}
	out := make([]RepairOp, 0, len(records))
	for i, rec := range records {
		op, perr := ParsePlanRow(rec)
		if perr != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, perr)
		}
		out = append(out, op)
	}
	return out, nil
}

// ApplyPlan runs operations that a plan produced — or a journal recorded — in
// batches, journaling each one that was applied (§6.5). It is `tie repair
// apply`. The DEST placeholder resolves to opts.Dest's UID on first use, the
// same way RepairTree resolves it, so a plan written against a fallback
// directory lands where the operator says it should.
func (tie *TieClient) ApplyPlan(ops []RepairOp, opts RepairOptions) (int, error) {
	if len(ops) == 0 {
		return 0, nil
	}
	if opts.Dest == "" {
		opts.Dest = FileURIScheme + "/restored/" + time.Now().Format("2006-01-02")
	}
	if !strings.HasPrefix(opts.Dest, FileURIScheme) {
		opts.Dest = FileURIScheme + "/" + opts.Dest
	}
	opts.Apply = true
	r := &repairSession{
		tie: tie, opts: opts, res: &RepairResult{},
		liveDir: map[string]bool{}, ghosts: map[string]bool{}, done: map[string]bool{},
		protected: map[string]bool{}, ancestor: map[string]string{}, handled: map[string]bool{},
	}
	if opts.Journal != nil {
		r.journal = opts.Journal
	}
	if err := r.run("apply", ops); err != nil {
		return r.res.Applied, err
	}
	return r.res.Applied, nil
}
