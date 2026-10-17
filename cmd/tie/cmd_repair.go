package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/uidbz/tie/client"

	"github.com/urfave/cli/v3"
)

// cmdRepair is the second and third stage of the fsck pipeline (§6.5):
//
//	tie verify | tie repair plan | tee plan.tsv | tie repair apply
//
// Each stage reads the previous one's stdout, so the plan can be inspected,
// saved, edited or replayed with standard tools before anything destructive
// happens. That replaces `verify --fix --dry-run --journal`, where the plan,
// the confirmation and the record were three mutually dependent flags behind
// one command.
func cmdRepair(a *app) *cli.Command {
	return &cli.Command{
		Name:  "repair",
		Usage: "Plan and apply triple repairs from a verify report or a plan file (the fsck pipeline)",
		Description: "plan reads the findings `tie verify` writes and answers the triple operations\n" +
			"that would fix them; apply reads those operations and runs them, echoing\n" +
			"the journal. A journal is a plan with a timestamp in front, so a journal\n" +
			"can be replayed with apply. Cycles and duplicate path claims are reported\n" +
			"by verify and never auto-fixed; index divergences are fixed by\n" +
			"`tie verify --index --repair`.",
		Commands: []*cli.Command{
			repairPlanCommand(a),
			repairApplyCommand(a),
		},
	}
}

func repairPlanCommand(a *app) *cli.Command {
	return &cli.Command{
		Name:      "plan",
		Usage:     "Turn findings on stdin into triple operations on stdout",
		ArgsUsage: "[findings.tsv]",
		Description: "Reads finding records (kind, subject, value1, value2) from a file or stdin —\n" +
			"what `tie verify` prints — and answers the operations that would fix them\n" +
			"(op, key, relation, value, note). It changes nothing. Exit 1 when the input\n" +
			"holds findings but none of them is fixable by a plan.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "collection", Usage: "Collection to plan against (default: config Collection)"},
			&cli.StringFlag{Name: "dest", Usage: "Fallback parent for children with no live ancestor (default: tie:/restored/<today>)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			src, closeSrc, err := inputReader(ctx)
			if err != nil {
				return exitError{code: exitUsage, msg: err.Error()}
			}
			defer closeSrc()

			findings, err := client.ReadFindings(src)
			if err != nil {
				return exitError{code: exitUsage, msg: fmt.Sprintf("repair plan: %v", err)}
			}
			if len(findings) == 0 {
				fmt.Fprintln(os.Stderr, "no findings on input: pipe `tie verify` into it, or name a findings file")
				return nil
			}

			scoped := a.tie.In(ctx.String("collection"))
			res, err := scoped.RepairTree(client.ReportFromFindings(findings), client.RepairOptions{
				Dest: ctx.String("dest"),
			})
			if err != nil {
				return operationalError("repair plan: %v", err)
			}

			o, err := a.outFor(ctx, "op", "key", "relation", "value", "note")
			if err != nil {
				return err
			}
			for _, op := range res.Planned {
				o.row(op.Kind, op.Key, op.Relation, op.Value, op.Note)
			}
			if err := o.flush(); err != nil {
				return operationalError("repair plan: %v", err)
			}

			fmt.Fprintf(os.Stderr, "planned %s from %s\n",
				pluralWith(len(res.Planned), "operation", "operations"),
				pluralWith(len(findings), "finding", "findings"))
			if len(res.Planned) == 0 {
				// Findings exist but nothing can be planned from them — cycles
				// and duplicate paths are the usual case. That is a problem the
				// operator still has, so it is not a success (fsck: 1).
				fmt.Fprintln(os.Stderr, "none of them is fixable by a plan: cycles and duplicate path claims need a human")
				return exitError{code: exitProblems, msg: "no operations planned"}
			}
			return nil
		},
	}
}

func repairApplyCommand(a *app) *cli.Command {
	return &cli.Command{
		Name:      "apply",
		Usage:     "Run the operations on stdin and echo the journal",
		ArgsUsage: "[plan.tsv]",
		Description: "Reads plan records (op, key, relation, value, note) — or a journal, which is the\n" +
			"same record with a timestamp in front — and applies them in batches. Every\n" +
			"applied operation is echoed to stdout as a journal record, and written to\n" +
			"--journal as well when a file is named. Nothing is applied until a batch\n" +
			"succeeds, so a journal never claims a mutation that did not happen.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "collection", Usage: "Collection to repair (default: config Collection)"},
			&cli.StringFlag{Name: "dest", Usage: "Directory the DEST placeholder resolves to (default: tie:/restored/<today>)"},
			&cli.StringFlag{Name: "journal", Usage: "Also write the journal to this file"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			src, closeSrc, err := inputReader(ctx)
			if err != nil {
				return exitError{code: exitUsage, msg: err.Error()}
			}
			defer closeSrc()

			ops, err := client.ReadPlan(src)
			if err != nil {
				return exitError{code: exitUsage, msg: fmt.Sprintf("repair apply: %v", err)}
			}
			if len(ops) == 0 {
				fmt.Fprintln(os.Stderr, "no operations on input: pipe `tie repair plan` into it, or name a plan file")
				return nil
			}

			// The journal is apply's product: stdout carries it, so the operator
			// can capture or replay exactly what happened (§6.2).
			journal := io.Writer(os.Stdout)
			if path := ctx.String("journal"); path != "" {
				f, err := os.Create(path)
				if err != nil {
					return operationalError("repair apply: creating journal: %v", err)
				}
				defer f.Close()
				journal = io.MultiWriter(os.Stdout, f)
				fmt.Fprintln(os.Stderr, "Journal:", path)
			}

			scoped := a.tie.In(ctx.String("collection"))
			applied, err := scoped.ApplyPlan(ops, client.RepairOptions{
				Dest:    ctx.String("dest"),
				Journal: journal,
			})
			if err != nil {
				return operationalError("repair apply: %v (%d operation(s) applied before it failed)", err, applied)
			}
			fmt.Fprintf(os.Stderr, "applied %s\n", pluralWith(applied, "operation", "operations"))
			return nil
		},
	}
}

// inputReader opens the file named by the first argument, or stdin when there
// is none or it is "-", so `tie repair plan < findings.tsv` and
// `tie repair plan findings.tsv` both work (§6.5).
func inputReader(ctx *cli.Command) (io.Reader, func(), error) {
	if ctx.Args().Len() == 0 || ctx.Args().First() == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(ctx.Args().First())
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { f.Close() }, nil
}
