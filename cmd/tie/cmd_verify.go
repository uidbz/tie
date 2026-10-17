package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/tiedb"
	"github.com/urfave/cli/v3"
)

// findingsOut is the renderer for `tie verify`'s report rows: kind, subject and
// two detail columns, the same schema for the index check and the tree scan so
// one stream can be cut, sorted and diffed (§8 S12).
func (a *app) findingsOut(ctx *cli.Command) (*out, error) {
	return a.outFor(ctx, "kind", "subject", "value1", "value2")
}

func cmdVerify(a *app) *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "Check the collection's index and virtual file tree for lost/incomplete nodes (fsck); exits non-zero if any problem is found",
		Description: "Verify first cross-checks the triplestore's forward and reverse association\n" +
			"indexes server-side (a divergence makes reverse queries — and therefore the tree\n" +
			"scan itself — unreliable), then scans the whole collection and reports structural\n" +
			"and metadata problems: orphaned directories/files (no parent edge — unreachable\n" +
			"from the root), dangling parent references, parent cycles, duplicate path claims,\n" +
			"files missing core metadata, and (with --check-blobs) files whose content is absent\n" +
			"from the filehost. It is read-only. --repair fixes the index in memory (adds lost\n" +
			"reverse entries, drops phantom reverse-only ones) and re-homes orphans under\n" +
			"tie:/restored/<date>/ — additive only. --fix additionally applies the destructive\n" +
			"repairs: dangling parent refs are re-parented to the nearest live ancestor, ghost\n" +
			"nodes (dir-typed, no path, no children, no tiedir-hash referrer, no blob) are\n" +
			"deleted, files get missing size/media-type/tie-type/filename re-derived from their\n" +
			"blob, and nameless leftovers or files whose blob is gone are deleted. Use\n" +
			"--fix --dry-run to see the plan first; every applied --fix mutation is journaled\n" +
			"as TSV (--journal). Take a `tie dump` before --fix. Cycles and duplicate paths\n" +
			"are always reported, never auto-fixed. --index runs only the index check; --deep\n" +
			"also validates every index position against its on-disk record (slow).",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "collection", Usage: "Collection to verify (default: config Collection)"},
			&cli.BoolFlag{Name: "repair", Usage: "Repair the index in memory and re-home orphaned files/dirs under a restored/ directory (default: report only)"},
			&cli.BoolFlag{Name: "check-blobs", Usage: "Also stat every file's content hash on the filehost (slow on a large store)"},
			&cli.BoolFlag{Name: "index", Usage: "Only cross-check the forward/reverse indexes; skip the tree scan"},
			&cli.BoolFlag{Name: "deep", Usage: "Index check also resolves every position against its on-disk record (slow; blocks writers meanwhile)"},
			&cli.StringFlag{Name: "dest", Usage: "Directory to restore orphans into (default: tie:/restored/<today>)"},
			&cli.BoolFlag{Name: "fix", Usage: "Also apply the destructive repairs (dangling refs, ghost nodes, file metadata, unrecoverable leftovers); implies --repair"},
			&cli.BoolFlag{Name: "dry-run", Usage: "With --fix: print the planned mutations and change nothing"},
			&cli.StringFlag{Name: "journal", Usage: "With --fix: TSV file receiving every applied mutation (default: tie-fix-journal-<collection>-<date>.tsv)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			collection := ctx.String("collection")
			// One scoped client: the index check, the tree scan and every
			// repair address the same collection (§3.8).
			scoped := a.tie.In(collection)
			fix := ctx.Bool("fix")
			dryRun := ctx.Bool("dry-run")
			if dryRun && !fix {
				return errors.New("--dry-run only applies to --fix")
			}
			// --fix implies the additive repairs; a dry run applies nothing at all.
			repair := (ctx.Bool("repair") || fix) && !dryRun

			// Index first: the tree scan is seeded by reverse queries, so an
			// inconsistent index would make it report phantoms and miss
			// children. With --repair the index is fixed before the scan, so
			// orphan re-homing never acts on a phantom.
			idx, err := scoped.CheckIndex(ctx.Bool("deep"), repair)
			if err != nil {
				return errors.New("Index Check Error: " + err.Error())
			}
			o, err := a.findingsOut(ctx)
			if err != nil {
				return err
			}
			printIndexReport(o, idx)
			if err := o.flush(); err != nil {
				return err
			}
			indexProblems := idx.Problems()
			if repair && idx.Repaired > 0 {
				fmt.Fprintf(os.Stderr, "Repaired %d index entr%s\n", idx.Repaired, plural(idx.Repaired, "y", "ies"))
				// Re-check so the exit code reflects the post-repair state.
				if post, err := scoped.CheckIndex(ctx.Bool("deep"), false); err == nil {
					indexProblems = post.Problems()
				}
			}
			if ctx.Bool("index") {
				if indexProblems > 0 {
					return cli.Exit(fmt.Sprintf("verify: %d index problem(s) found", indexProblems), 1)
				}
				fmt.Fprintf(os.Stderr, "OK: index consistent (%d forward, %d reverse entries)\n", idx.ForwardEntries, idx.ReverseEntries)
				return nil
			}
			if indexProblems > 0 {
				fmt.Fprintln(os.Stderr, "Warning: index inconsistent — tree results below may include phantoms or miss children; run `tie verify --repair`")
			}

			rep, err := scoped.Verify(ctx.Bool("check-blobs"))
			if err != nil && rep == nil {
				return errors.New("Verify Error: " + err.Error())
			}
			o, err = a.findingsOut(ctx)
			if err != nil {
				return err
			}
			printVerifyReport(o, rep)
			if err := o.flush(); err != nil {
				return err
			}
			if err != nil {
				// Partial report (blob check failed after structural checks).
				return errors.New("Verify Error: " + err.Error())
			}
			// Guard-rail: the tree scan is seeded by reverse queries, and
			// repair's heuristics depend on it being complete. With the index
			// still inconsistent, a destructive repair could act on phantoms
			// or miss children, so refuse it here (§9).
			if repairBlockedByIndex(indexProblems, repair, fix, dryRun) {
				return cli.Exit(fmt.Sprintf(
					"verify: refusing to repair: the index check reported %d problem(s). "+
						"Fix the index first (`tie verify --index --repair`): repairing on an "+
						"inconsistent index can act on phantoms or miss children.", indexProblems), 1)
			}

			changed := false
			if repair {
				n, rerr := scoped.RepairOrphans(rep, ctx.String("dest"))
				if rerr != nil {
					return errors.New("Repair Error: " + rerr.Error())
				}
				if n > 0 {
					fmt.Fprintf(os.Stderr, "Restored %d orphaned node(s) under %s\n", n, restoreDestDisplay(ctx.String("dest")))
					changed = true
				}
			}
			if fix {
				opts := client.RepairOptions{Dest: ctx.String("dest"), Apply: !dryRun, Progress: os.Stderr}
				if !dryRun {
					path := ctx.String("journal")
					if path == "" {
						name := collection
						if name == "" {
							name = scoped.CollectionInfo().CollectionId
						}
						path = fmt.Sprintf("tie-fix-journal-%s-%s.tsv", name, time.Now().Format("2006-01-02T150405"))
					}
					f, ferr := os.Create(path)
					if ferr != nil {
						return errors.New("Fix Error: creating journal: " + ferr.Error())
					}
					defer f.Close()
					opts.Journal = f
					fmt.Fprintf(os.Stderr, "Journal: %s\n", path)
				}
				res, ferr := scoped.RepairTree(rep, opts)
				if dryRun {
					// The plan is data: it goes to stdout through the same
					// encoder, untruncated, and `tie verify --fix --dry-run |
					// tie repair apply` is the point (§6.5).
					plan, perr := a.outFor(ctx, "op", "key", "relation", "value", "note")
					if perr != nil {
						return perr
					}
					for _, op := range res.Planned {
						plan.row(op.Kind, op.Key, op.Relation, op.Value, op.Note)
					}
					if perr := plan.flush(); perr != nil {
						return perr
					}
					fmt.Fprintf(os.Stderr, "dry-run: %d mutation(s) planned; re-run without --dry-run to apply\n", len(res.Planned))
				} else if res.Applied > 0 {
					fmt.Fprintf(os.Stderr, "Applied %d mutation(s)\n", res.Applied)
					changed = true
				}
				if ferr != nil {
					return errors.New("Fix Error: " + ferr.Error())
				}
			}
			if changed {
				// Re-verify so the reported problem count and exit code
				// reflect the post-repair state, not the pre-repair scan.
				rep, err = scoped.Verify(ctx.Bool("check-blobs"))
				if err != nil {
					return errors.New("Verify Error: " + err.Error())
				}
				o, err = a.findingsOut(ctx)
				if err != nil {
					return err
				}
				printVerifyReport(o, rep)
				if err := o.flush(); err != nil {
					return err
				}
			}
			if total := rep.Problems() + indexProblems; total > 0 {
				return cli.Exit(fmt.Sprintf("verify: %d problem(s) found", total), 1)
			}
			fmt.Fprintf(os.Stderr, "OK: %d directories, %d files — no problems\n", rep.DirCount, rep.FileCount)
			return nil
		},
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// repairBlockedByIndex decides whether `tie verify` may run a destructive
// repair step. The tree scan is seeded by reverse queries and repair's
// heuristics (ghost detection, orphan re-homing) depend on that scan being
// complete, so with the index still inconsistent a repair could act on
// phantoms or miss children (§9). A --fix --dry-run is not blocked: printing a
// plan changes nothing, and seeing the plan is how the operator decides what to
// fix first.
func repairBlockedByIndex(indexProblems int, repair, fix, dryRun bool) bool {
	if indexProblems <= 0 {
		return false
	}
	return repair || (fix && !dryRun)
}

// printIndexReport writes the server-side index cross-check as findings rows
// (§8 S12): the same four columns the tree report uses, so one `tie verify`
// stream can be cut, sorted and diffed. A count row carries "(count)" as its
// subject; the samples that follow carry the offending triple.
func indexReportRows(rep *tiedb.IndexReport) [][]string {
	var rows [][]string
	count := func(kind string, n int) {
		if n > 0 {
			rows = append(rows, []string{kind, "(count)", strconv.Itoa(n), ""})
		}
	}
	count(tiedb.IndexMissingReverse, rep.MissingReverse)
	count(tiedb.IndexReverseOnly, rep.ReverseOnly)
	count(tiedb.IndexPositionMismatch, rep.PositionMismatch)
	count(tiedb.IndexMisresolved, rep.Misresolved)
	for _, s := range rep.Samples {
		rows = append(rows, []string{s.Kind, s.Key, s.Value1, s.Value2})
	}
	sortRows(rows)
	return rows
}

func printIndexReport(o *out, rep *tiedb.IndexReport) {
	for _, row := range indexReportRows(rep) {
		o.row(row...)
	}
}

// restoreDestDisplay names the directory orphans are re-homed under, for the
// progress line that tells the operator where they went.
func restoreDestDisplay(dest string) string {
	if dest != "" {
		return dest
	}
	return "tie:/restored/" + time.Now().Format("2006-01-02")
}

// printVerifyReport writes the tree findings as rows — kind, subject, value1,
// value2 — through the shared renderer (§8 S12). They used to be hand-aligned
// prose on stderr, which meant the report, the command's actual product, could
// not be piped; and the classes came out of maps in whatever order Go iterated
// them, so two runs of one store did not agree.
func verifyReportRows(rep *client.VerifyReport) [][]string {
	var rows [][]string
	for _, u := range rep.OrphanDirs {
		rows = append(rows, []string{"orphan-dir", string(u), "", ""})
	}
	for _, h := range rep.OrphanFiles {
		rows = append(rows, []string{"orphan-file", h, "", ""})
	}
	for _, e := range rep.DanglingParentRefs {
		rows = append(rows, []string{"dangling-parent", e.Child, e.Parent, ""})
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
		rows = append(rows, []string{"cycle", subject, strings.Join(parts, " -> "), ""})
	}
	for p, uids := range rep.DuplicatePaths {
		parts := make([]string, len(uids))
		for i, u := range uids {
			parts[i] = string(u)
		}
		rows = append(rows, []string{"duplicate-path", p, strings.Join(parts, ", "), ""})
	}
	for _, g := range rep.MissingMetadata {
		kind := "file"
		if g.IsDir {
			kind = "dir"
		}
		rows = append(rows, []string{"missing-metadata", string(g.Subject), kind, strings.Join(g.Missing, ", ")})
	}
	for _, h := range rep.MissingBlobs {
		rows = append(rows, []string{"missing-blob", h, "", ""})
	}
	sortRows(rows)
	return rows
}

func printVerifyReport(o *out, rep *client.VerifyReport) {
	for _, row := range verifyReportRows(rep) {
		o.row(row...)
	}
}
