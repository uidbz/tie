package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/uidbz/tie/runclient"

	"github.com/urfave/cli/v3"
)

func cmdStatus() *cli.Command {
	return &cli.Command{
		Name:        "status",
		Aliases:     []string{"s"},
		Usage:       "tie-run status jobID [--runner URL] [--json]",
		Description: "Show status of a job. Without --runner the job is looked up across the fleet.",
		Flags: []cli.Flag{
			runnerFlag(),
			serversFlag(),
			&cli.BoolFlag{Name: "follow", Value: true},
			jsonFlag(),
		},

		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return errors.New("tie-run status needs exactly 1 argument: tie-run status jobID")
			}
			// --runner is normalised (bare hosts get https://); without it
			// the job is located across the fleet instead of only on the
			// seed runner, so jobs on other runners are found.
			runner, err := resolveRunner(cmd, cmd.Args().First())
			if err != nil {
				return err
			}
			if debugMode {
				fmt.Fprintln(os.Stderr, "!! Debug mode activated - messaging more verbose !!")
			}

			// --json: one-shot JSON of the same JobStatus the human view
			// prints — no progress bar, no percent spam on stdout, so the
			// output stays parseable.
			if cmd.Bool("json") {
				status, err := runclient.GetStatus(runner, cmd.Args().First())
				if err != nil {
					return err
				}
				return printJSON(status)
			}

			// ShowProgressCtx threads the app context so Ctrl-C stops the
			// watch with a resume hint instead of killing the process.
			status, err := runclient.ShowProgressCtx(ctx, runner, cmd.Args().First(), false, cmd.Bool("follow"))
			if err != nil {
				// Return before printing: an errored status has no
				// RunningTime worth showing.
				return err
			}

			if debugMode {
				fmt.Println("Log:")
				fmt.Println(status.Log)
				if status.ErrorMsg != "" {
					fmt.Fprintln(os.Stderr, "Error message:")
					fmt.Fprintln(os.Stderr, status.ErrorMsg)
				}
				fmt.Println("Handler:", status.Handler)
				fmt.Println("InputHash:", status.InputHash)
				fmt.Println("OutputHash:", status.OutputHash)
				fmt.Println("JobType:", status.JobType)
				fmt.Println("RunningTime:", status.RunningTime)
			} else {
				fmt.Println("RunningTime:", status.RunningTime)
			}

			return nil
		},
	}
}
