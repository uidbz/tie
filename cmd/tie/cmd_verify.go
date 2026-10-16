package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/tiedb"
	"github.com/urfave/cli/v3"
)

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
			printIndexReport(idx)
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
			printVerifyReport(rep)
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
					for _, o := range res.Planned {
						fmt.Printf("%-6s %s %-11s %s   # %s\n", o.Kind, o.Key, o.Relation, shortVal(o.Value), o.Note)
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
				printVerifyReport(rep)
			}
			if total := rep.Problems() + indexProblems; total > 0 {
				return cli.Exit(fmt.Sprintf("verify: %d problem(s) found", total), 1)
			}
			fmt.Fprintf(os.Stderr, "OK: %d directories, %d files — no problems\n", rep.DirCount, rep.FileCount)
			return nil
		},
	}
}

func shortVal(s string) string {
	if len(s) > 48 {
		return s[:48] + "…"
	}
	return s
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

// printIndexReport writes the server-side index cross-check to stderr: one
// line per problem class with a count, then the sampled entries.
func printIndexReport(rep *tiedb.IndexReport) {
	w := os.Stderr
	if rep.Problems() == 0 {
		return
	}
	mode := ""
	if rep.Deep {
		mode = ", deep"
	}
	fmt.Fprintf(w, "Index inconsistencies (%d forward, %d reverse entries%s):\n", rep.ForwardEntries, rep.ReverseEntries, mode)
	line := func(label string, n int) {
		if n > 0 {
			fmt.Fprintf(w, "  %-52s %d\n", label, n)
		}
	}
	line("forward triples missing their reverse entry:", rep.MissingReverse)
	line("reverse-only entries (phantoms, no forward triple):", rep.ReverseOnly)
	line("forward/reverse position mismatches:", rep.PositionMismatch)
	line("index positions whose record does not match:", rep.Misresolved)
	if len(rep.Samples) > 0 {
		fmt.Fprintf(w, "  samples (%d):\n", len(rep.Samples))
		for _, s := range rep.Samples {
			fmt.Fprintf(w, "    %-18s %s  %s  %s\n", s.Kind, s.Key, s.Value1, s.Value2)
		}
	}
}

// restoreDestDisplay renders the effective restore directory for the summary
// line, mirroring RepairOrphans' default when --dest is empty.
func restoreDestDisplay(dest string) string {
	if dest != "" {
		return dest
	}
	return "tie:/restored/" + time.Now().Format("2006-01-02")
}

// printVerifyReport writes each non-empty problem class to stderr. The summary
// counts and OK line are printed separately by the caller; a fully-clean report
// prints nothing here.
func printVerifyReport(rep *client.VerifyReport) {
	w := os.Stderr
	section := func(title string, n int) {
		if n > 0 {
			fmt.Fprintf(w, "%s (%d):\n", title, n)
		}
	}
	section("Orphaned directories (no parent — unreachable)", len(rep.OrphanDirs))
	for _, u := range rep.OrphanDirs {
		fmt.Fprintf(w, "  %s\n", u)
	}
	section("Orphaned files (no parent — unreachable)", len(rep.OrphanFiles))
	for _, h := range rep.OrphanFiles {
		fmt.Fprintf(w, "  %s\n", h)
	}
	section("Dangling parent references (parent no longer exists)", len(rep.DanglingParentRefs))
	for _, e := range rep.DanglingParentRefs {
		fmt.Fprintf(w, "  %s -> %s\n", e.Child, e.Parent)
	}
	section("Parent cycles (a directory is its own ancestor)", len(rep.Cycles))
	for _, c := range rep.Cycles {
		parts := make([]string, len(c))
		for i, u := range c {
			parts[i] = string(u)
		}
		fmt.Fprintf(w, "  %s\n", strings.Join(parts, " -> "))
	}
	if len(rep.DuplicatePaths) > 0 {
		fmt.Fprintf(w, "Duplicate path claims (%d paths):\n", len(rep.DuplicatePaths))
		paths := make([]string, 0, len(rep.DuplicatePaths))
		for p := range rep.DuplicatePaths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			uids := rep.DuplicatePaths[p]
			parts := make([]string, len(uids))
			for i, u := range uids {
				parts[i] = string(u)
			}
			fmt.Fprintf(w, "  %s : %s\n", p, strings.Join(parts, ", "))
		}
	}
	section("Missing metadata (incomplete import)", len(rep.MissingMetadata))
	for _, g := range rep.MissingMetadata {
		kind := "file"
		if g.IsDir {
			kind = "dir "
		}
		fmt.Fprintf(w, "  %s %s : missing %s\n", kind, g.Subject, strings.Join(g.Missing, ", "))
	}
	section("Missing blobs (content absent from filehost)", len(rep.MissingBlobs))
	for _, h := range rep.MissingBlobs {
		fmt.Fprintf(w, "  %s\n", h)
	}
}
