package main

// Pipelines are submitted from scripts (runclient.SubmitPipeline); from
// the shell you list, inspect, cancel and resume them. A pipeline lives
// on the runner that accepted it, so every command looks its ID up
// across the fleet.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"

	"github.com/urfave/cli/v3"
)

func cmdPipeline() *cli.Command {
	return &cli.Command{
		Name:  "pipeline",
		Usage: "tie-run pipeline <list|show|cancel|resume>",
		Description: "Inspect and control pipelines: step graphs the runner sequences on its own. " +
			"Cancel and resume require the entry step's DeployKey (from " + projectFile + " or tie-run-config.toml).",
		Commands: []*cli.Command{
			cmdPipelineList(),
			cmdPipelineShow(),
			cmdPipelineCancel(),
			cmdPipelineResume(),
		},
	}
}

func cmdPipelineList() *cli.Command {
	return &cli.Command{
		Name:        "list",
		Aliases:     []string{"ls"},
		Usage:       "tie-run pipeline list [--since 30d] [--user me] [--status running] [--servers a,b] [--json]",
		Description: "List pipelines across all runners, newest first.",
		Flags: []cli.Flag{
			serversFlag(),
			sinceFlag(),
			userFlag(),
			&cli.StringFlag{Name: "status", Usage: "Filter by status: running|done|failed|cancelled"},
			jsonFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}
			userFlt := cmd.String("user")
			if userFlt == "me" {
				userFlt = runclient.CurrentUser()
			}

			sinceDur, err := parseSinceDuration(cmd.String("since"))
			if err != nil {
				return err
			}
			pipelines, errs := runclient.ListPipelinesAll(srv, sinceDur, userFlt, cmd.String("status"))
			reportErrors(errs)

			if cmd.Bool("json") {
				return printJSON(pipelines)
			}

			if len(pipelines) == 0 {
				fmt.Println("No pipelines found.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tPIPELINE\tNAME\tUSER\tSTATUS\tDONE\tCREATED\tFINISHED")
			for _, k := range pipelines {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n",
					shortServer(k.Server), k.Value.PipelineID, k.Value.Name, k.Value.User, k.Value.Status,
					doneSteps(k.Value), len(k.Value.Steps), fmtUnix(k.Value.CreatedUnix), fmtUnix(k.Value.FinishedUnix))
			}
			tw.Flush()
			return nil
		},
	}
}

// doneSteps counts the steps that finished successfully, for the list's
// DONE column.
func doneSteps(p *api.Pipeline) int {
	n := 0
	for _, s := range p.Steps {
		if s.Status == api.StatusDone {
			n++
		}
	}
	return n
}

func cmdPipelineShow() *cli.Command {
	return &cli.Command{
		Name:    "show",
		Aliases: []string{"get"},
		Usage:   "tie-run pipeline show pipelineID [--follow] [--download step --dest DIR] [--json]",
		Description: "Show a pipeline with per-step state, looking it up across the fleet. " +
			"Follows step transitions until the run reaches a terminal state; use --follow=false for a one-shot view.",
		Flags: []cli.Flag{
			serversFlag(),
			&cli.BoolFlag{Name: "follow", Value: true},
			&cli.StringFlag{Name: "download", Usage: "Download the named step's output (dest defaults to .)"},
			// --dest names the download target explicitly; the positional
			// dest after the ID still works but is easy to misuse. The
			// flag wins.
			&cli.StringFlag{Name: "dest", Usage: "Target directory for --download (default .)"},
			jsonFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() < 1 {
				return errors.New("tie-run pipeline show needs a pipeline ID: tie-run pipeline show <pipelineID>")
			}
			id := cmd.Args().First()
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}

			found, err := runclient.FindPipeline(srv, id)
			if err != nil {
				return err
			}
			p, runner := found.Value, found.Server

			download := cmd.String("download")
			if download != "" {
				dest := cmd.String("dest")
				if dest == "" && cmd.Args().Len() > 1 {
					dest = cmd.Args().Get(1) // positional dest still works
				}
				if dest == "" {
					dest = "."
				}
				return downloadPipelineStep(p, runner, download, dest)
			}

			if cmd.Bool("json") {
				return printJSON(p)
			}

			printPipeline(p, runner)

			if !cmd.Bool("follow") || runclient.PipelineDone(&p) {
				return nil
			}
			// WaitPipelineCtx threads the app context so Ctrl-C stops the
			// wait with a resume hint.
			final, err := runclient.WaitPipelineCtx(ctx, runner, p.PipelineID, 3*time.Second, func(step *api.PipelineStep) {
				fmt.Printf("  %-24s %s\n", step.Name, step.Status)
			})
			if err != nil {
				return err
			}
			printPipeline(final, runner)
			return nil
		},
	}
}

// printPipeline renders a pipeline header and one row per step, in
// ordinal order (seeds first), the same order the runner stores them in.
func printPipeline(p api.Pipeline, runner string) {
	fmt.Printf("Pipeline %s  %s\n", p.PipelineID, p.Name)
	fmt.Printf("User: %s  Status: %s  Runner: %s  Created: %s  Finished: %s\n",
		p.User, p.Status, shortServer(runner), fmtUnix(p.CreatedUnix), fmtUnix(p.FinishedUnix))
	if p.ErrorMsg != "" {
		fmt.Println("Error:", p.ErrorMsg)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ORD\tSTEP\tSTATUS\tJOB\tHANDLER\tOUTPUT\tERROR\tSTARTED\tFINISHED")
	for _, s := range p.Steps {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Ordinal, s.Name, s.Status, s.JobID, s.Handler, shortHash(s.OutputHash), s.ErrorMsg,
			fmtUnix(s.StartedUnix), fmtUnix(s.FinishedUnix))
	}
	tw.Flush()
}

// downloadPipelineStep fetches a step's recorded output from the runner's
// filehost into dest, mirroring downloadJob for plain jobs.
func downloadPipelineStep(p api.Pipeline, runner, step, dest string) error {
	s := runclient.Step(&p, step)
	if s == nil {
		return fmt.Errorf("pipeline %s has no step %q", p.PipelineID, step)
	}
	if s.OutputHash == "" {
		return fmt.Errorf("step %q has no output to download (status: %s)", step, s.Status)
	}
	filehost, err := runclient.GetFilehost(runner)
	if err != nil {
		return fmt.Errorf("could not determine filehost: %w", err)
	}
	return runclient.Download(filehost, s.OutputHash, dest)
}

func cmdPipelineCancel() *cli.Command {
	return &cli.Command{
		Name:  "cancel",
		Usage: "tie-run pipeline cancel [pipelineID] [--servers a,b]",
		Description: "Cancel a running pipeline. With no ID, pick interactively from running pipelines. " +
			"Requires the entry step's DeployKey (from " + projectFile + " or tie-run-config.toml).",
		Flags: []cli.Flag{
			serversFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}

			var target runclient.Keyed[api.Pipeline]
			if id := cmd.Args().First(); id != "" {
				p, err := runclient.FindPipeline(srv, id)
				if err != nil {
					return err
				}
				target = p
			} else {
				p, err := pickPipeline(srv)
				if err != nil {
					return err
				}
				target = p
			}

			if target.Value.Status != api.StatusRunning {
				return fmt.Errorf("pipeline %s is already %s", target.Value.PipelineID, target.Value.Status)
			}

			key, err := pipelineKey(&target.Value)
			if err != nil {
				return err
			}
			if err := runclient.CancelPipeline(target.Server, target.Value.PipelineID, key); err != nil {
				return err
			}
			fmt.Printf("✓ Cancelled pipeline %s (%s) on %s\n", target.Value.Name, target.Value.PipelineID, shortServer(target.Server))
			return nil
		},
	}
}

// pickPipeline presents an interactive chooser over running pipelines
// across the given servers, mirroring pickJob for jobs.
func pickPipeline(servers []string) (runclient.Keyed[api.Pipeline], error) {
	pipelines, errs := runclient.ListPipelinesAll(servers, 0, "", api.StatusRunning)
	reportErrors(errs)
	if len(pipelines) == 0 {
		return runclient.Keyed[api.Pipeline]{}, errors.New("no running pipelines to cancel")
	}

	labels := make([]string, len(pipelines))
	for i, k := range pipelines {
		labels[i] = fmt.Sprintf("%s  %s  user=%s  %s", k.Value.Name, k.Value.PipelineID, k.Value.User, shortServer(k.Server))
	}
	idx, err := pickFrom("Select pipeline to cancel:", labels)
	if err != nil {
		return runclient.Keyed[api.Pipeline]{}, err
	}
	return runclient.Keyed[api.Pipeline]{Server: pipelines[idx].Server, Value: *pipelines[idx].Value}, nil
}

func cmdPipelineResume() *cli.Command {
	return &cli.Command{
		Name:  "resume",
		Usage: "tie-run pipeline resume pipelineID [--from step] [--with step=hash ...] [--servers a,b]",
		Description: "Re-run the part of a finished pipeline that did not complete. --from re-runs the " +
			"named step and everything downstream of it; --with takes a step's output from a supplied " +
			"tree hash instead of running it. Requires the entry step's DeployKey (from " + projectFile + " or tie-run-config.toml).",
		Flags: []cli.Flag{
			serversFlag(),
			&cli.StringFlag{Name: "from", Usage: "Re-run this step and everything downstream of it"},
			&cli.StringSliceFlag{Name: "with", Usage: "Take a step's output from a tree hash: --with step=hash (repeatable)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return errors.New("tie-run pipeline resume needs exactly 1 argument: tie-run pipeline resume <pipelineID>")
			}
			id := cmd.Args().First()
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}

			found, err := runclient.FindPipeline(srv, id)
			if err != nil {
				return err
			}

			overrides, err := parseOverrides(cmd.StringSlice("with"))
			if err != nil {
				return err
			}

			key, err := pipelineKey(&found.Value)
			if err != nil {
				return err
			}
			resumed, err := runclient.ResumePipeline(found.Server, found.Value.PipelineID, key, cmd.String("from"), overrides)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Resumed pipeline %s (%s) on %s\n", resumed.Name, resumed.PipelineID, shortServer(found.Server))
			printPipeline(resumed, found.Server)
			return nil
		},
	}
}

// parseOverrides turns --with step=hash pairs into the resume override
// map.
func parseOverrides(with []string) (map[string]string, error) {
	if len(with) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(with))
	for _, pair := range with {
		name, hash, ok := strings.Cut(pair, "=")
		if !ok || name == "" || hash == "" {
			return nil, fmt.Errorf("--with expects step=hash, got %q", pair)
		}
		out[name] = hash
	}
	return out, nil
}

// pipelineKey looks up the DeployKey the runner requires for cancel and
// resume: the key of the pipeline's entry step image.
func pipelineKey(p *api.Pipeline) (string, error) {
	entry := runclient.EntryHandler(p)
	if entry == "" {
		return "", fmt.Errorf("pipeline %s has no step that runs an image, so there is no DeployKey to authorize with", p.PipelineID)
	}
	return deployKeyFor(entry)
}
