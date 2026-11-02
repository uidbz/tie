package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"

	"github.com/urfave/cli/v3"
)

func cmdJobs() *cli.Command {
	return &cli.Command{
		Name:        "jobs",
		Aliases:     []string{"j"},
		Usage:       "tie-run jobs [--since 30d] [--user me] [--status done] [--servers a,b] [--json]",
		Description: "List historic jobs across all runners. Use --download <jobID> <dest> to fetch a job's results.",
		Flags: []cli.Flag{
			serversFlag(),
			sinceFlag(),
			userFlag(),
			&cli.StringFlag{Name: "status", Usage: "Filter by status: queued|running|done|failed|cancelled"},
			&cli.StringFlag{Name: "download", Usage: "Download results of the given jobID"},
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
			jobs, errs := runclient.ListJobsAll(srv, sinceDur, userFlt, cmd.String("status"))
			reportErrors(errs)

			if download := cmd.String("download"); download != "" {
				return downloadJob(jobs, download, cmd.Args().First())
			}

			if cmd.Bool("json") {
				return printJSON(jobs)
			}

			if len(jobs) == 0 {
				fmt.Println("No jobs found.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tJOB ID\tUSER\tHANDLER\tSTATUS\tEXIT\tMEM\tCREATED\tRUNTIME")
			for _, j := range jobs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					shortServer(j.Server), j.Value.JobID, j.Value.User, j.Value.Handler, j.Value.Status,
					fmtExitReason(j.Value.ExitReason), fmtMemMB(j.Value.MemPeakMB), fmtUnix(j.Value.CreatedUnix), j.Value.RunningTime)
			}
			tw.Flush()
			return nil
		},
	}
}

// downloadJob finds a job by ID among the listed jobs and downloads its
// output from the runner's filehost into dest (default "." if empty).
func downloadJob(jobs []runclient.Keyed[*api.JobStatus], jobID, dest string) error {
	if dest == "" {
		dest = "."
	}
	for _, k := range jobs {
		if k.Value.JobID != jobID {
			continue
		}
		if k.Value.OutputHash == "" {
			return fmt.Errorf("job %s has no output (status: %s)", jobID, k.Value.Status)
		}
		filehost := k.Value.Filehost
		if filehost == "" {
			// Fall back to asking the reporting server for its filehost.
			fh, err := runclient.GetFilehost(k.Server)
			if err != nil {
				return fmt.Errorf("could not determine filehost: %w", err)
			}
			filehost = fh
		}
		return runclient.Download(filehost, k.Value.OutputHash, dest)
	}
	return fmt.Errorf("job %s not found in the listed jobs (widen --since or check --servers)", jobID)
}
