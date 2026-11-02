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

func cmdQueue() *cli.Command {
	return &cli.Command{
		Name:        "queue",
		Aliases:     []string{"q"},
		Usage:       "tie-run queue [--servers a,b] [--json]",
		Description: "Show queued (waiting) jobs across all runners.",
		Flags: []cli.Flag{
			serversFlag(),
			jsonFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}
			perServer, errs := runclient.QueueAll(srv)
			reportErrors(errs)

			if cmd.Bool("json") {
				// Same jobs the table prints, flattened in table order.
				var flat []*api.JobStatus
				for _, server := range srv {
					flat = append(flat, perServer[server]...)
				}
				return printJSON(flat)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tPOS\tJOB ID\tUSER\tHANDLER\tCREATED")
			total := 0
			for _, server := range srv {
				jobs := perServer[server]
				for i, j := range jobs {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
						shortServer(server), i+1, j.JobID, j.User, j.Handler, fmtUnix(j.CreatedUnix))
					total++
				}
			}
			tw.Flush()
			if total == 0 {
				fmt.Println("All queues are empty.")
			}
			return nil
		},
	}
}

func cmdActive() *cli.Command {
	return &cli.Command{
		Name:        "active",
		Aliases:     []string{"a"},
		Usage:       "tie-run active [--servers a,b] [--json]",
		Description: "Show the jobs each runner is actively processing.",
		Flags: []cli.Flag{
			serversFlag(),
			jsonFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}
			perServer, errs := runclient.ActiveAll(srv)
			reportErrors(errs)

			if cmd.Bool("json") {
				// Same jobs the table prints, flattened in table order.
				var flat []*api.JobStatus
				for _, server := range srv {
					flat = append(flat, perServer[server]...)
				}
				return printJSON(flat)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tJOB ID\tUSER\tHANDLER\tTYPE\tCPU\tMEM\tPERCENT\tRUNTIME")
			for _, server := range srv {
				jobs := perServer[server]
				if len(jobs) == 0 {
					fmt.Fprintf(tw, "%s\t-\t-\t(idle)\t-\t-\t-\t-\t-\n", shortServer(server))
					continue
				}
				for _, j := range jobs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.0f%%\t%s\n",
						shortServer(server), j.JobID, j.User, j.Handler, j.JobType,
						fmtCPU(j.ReqCPU), fmtMemMB(int64(j.ReqRAMMB)), j.Percent, j.RunningTime)
				}
			}
			tw.Flush()
			return nil
		},
	}
}
