package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/uidbz/tie/runclient"

	"github.com/urfave/cli/v3"
)

func cmdLogs() *cli.Command {
	return &cli.Command{
		Name:        "logs",
		Aliases:     []string{"l"},
		Usage:       "tie-run logs <jobID> [--runner URL] — without --runner the job is looked up across the fleet",
		Description: "Fetch and display logs from a job",
		Flags: []cli.Flag{
			runnerFlag(),
			serversFlag(),
			&cli.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "Follow log output (poll for updates)"},
			&cli.BoolFlag{Name: "header", Usage: "Show job metadata header", Value: true},
		},

		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return errors.New("tie-run logs needs exactly 1 argument: jobID")
			}
			// --runner is normalised (bare hosts get https://); without it
			// the job is located across the fleet instead of only on the
			// seed runner.
			runner, err := resolveRunner(cmd, cmd.Args().First())
			if err != nil {
				return err
			}

			jobID := cmd.Args().First()
			showHeader := cmd.Bool("header")

			if cmd.Bool("follow") {
				// Follow mode - keep polling for updates. ctx is the app
				// context, cancelled on Ctrl-C/SIGTERM by main.
				return followLogs(ctx, runner, jobID, showHeader)
			}

			// Single fetch mode
			status, err := runclient.GetStatus(runner, jobID)
			if err != nil {
				return err
			}

			// Show header with job metadata
			if showHeader {
				fmt.Printf("JobID: %s\n", status.JobID)
				fmt.Printf("Handler: %s\n", status.Handler)
				fmt.Printf("JobType: %s\n", status.JobType)
				fmt.Printf("Status: ")
				if status.Done {
					fmt.Println("Done")
				} else if status.IsRunning {
					fmt.Println("Running")
				} else {
					fmt.Println("Waiting")
				}
				fmt.Printf("Progress: %.1f%%\n", status.Percent)
				if status.RunningTime != "" {
					fmt.Printf("RunningTime: %s\n", status.RunningTime)
				}
				fmt.Println("---")
			}

			// Display the log
			if status.Log != "" {
				fmt.Println(status.Log)
			} else {
				fmt.Println("No logs available for job:", jobID)
			}

			// Show error message if present. The job's own error text is
			// data about the job, but the "Error:" block is an error
			// report: it goes to stderr so `tie-run logs` output stays
			// pipeable.
			if status.ErrorMsg != "" {
				fmt.Fprintln(os.Stderr, "\nError:")
				fmt.Fprintln(os.Stderr, status.ErrorMsg)
			}

			return nil
		},
	}
}

// followLogs polls a job's status and prints new log content until the
// job is done. Transient status errors are tolerated with exponential
// backoff (1s -> 30s, up to 10 consecutive failures) so a runner restart
// or Wi-Fi blip does not kill the follow of a job that keeps running;
// after the budget is spent the error names how to resume. If the runner
// truncates or rotates the log — status.Log shrinks below what we already
// printed — the follow prints a marker and reprints from the start
// instead of going silent. A cancelled ctx (Ctrl-C via main's
// signal.NotifyContext) exits with the resume hint and a nil error: the
// job is unaffected.
func followLogs(ctx context.Context, runner, jobID string, showHeader bool) error {
	lastLogLength := 0
	headerShown := false
	consecutiveErrs := 0
	backoff := time.Second
	const (
		maxConsecutiveErrs = 10
		maxBackoff         = 30 * time.Second
	)

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("watching cancelled; job %s still running on %s — resume with tie-run status %s\n", jobID, runner, jobID)
			return nil
		default:
		}

		status, err := runclient.GetStatus(runner, jobID)
		if err != nil {
			consecutiveErrs++
			if consecutiveErrs >= maxConsecutiveErrs {
				return fmt.Errorf("job %s is still on %s; check with tie-run status %s", jobID, runner, jobID)
			}
			select {
			case <-ctx.Done():
				fmt.Printf("watching cancelled; job %s still running on %s — resume with tie-run status %s\n", jobID, runner, jobID)
				return nil
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		consecutiveErrs = 0
		backoff = time.Second

		// Show header once
		if showHeader && !headerShown {
			fmt.Printf("JobID: %s\n", status.JobID)
			fmt.Printf("Handler: %s\n", status.Handler)
			fmt.Printf("JobType: %s\n", status.JobType)
			fmt.Println("Following logs... (Ctrl+C to stop)")
			fmt.Println("---")
			headerShown = true
		}

		// Log truncated/rotated by the runner: shorter than what we
		// already printed. Without this the slice below would print
		// nothing forever.
		if len(status.Log) < lastLogLength {
			fmt.Println("--- log truncated by runner, reprinting from start ---")
			lastLogLength = 0
		}

		// Print only new log content
		if len(status.Log) > lastLogLength {
			fmt.Print(status.Log[lastLogLength:])
			lastLogLength = len(status.Log)
		}

		// Exit when job is done
		if status.Done {
			if status.ErrorMsg != "" {
				fmt.Fprintln(os.Stderr, "\nError:")
				fmt.Fprintln(os.Stderr, status.ErrorMsg)
			}
			fmt.Println("\nJob completed.")
			break
		}

		select {
		case <-ctx.Done():
			fmt.Printf("watching cancelled; job %s still running on %s — resume with tie-run status %s\n", jobID, runner, jobID)
			return nil
		case <-time.After(1 * time.Second):
		}
	}

	return nil
}
