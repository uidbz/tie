package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/version"

	"github.com/urfave/cli/v3"
)

func cmdCapacity() *cli.Command {
	return &cli.Command{
		Name:        "capacity",
		Aliases:     []string{"cap"},
		Usage:       "tie-run capacity [--servers a,b]",
		Description: "Show each runner's resource pool (CPU/RAM/GPU), running and queued counts.",
		Flags: []cli.Flag{
			serversFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}
			caps, errs := runclient.CapabilitiesAll(srv)
			reportErrors(errs)

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tVERSION\tCPU (free/total)\tMEM (free/total)\tGPU (free/total)\tRUNNING\tQUEUED\tCAPABILITIES")
			for _, server := range srv {
				c, ok := caps[server]
				if !ok {
					continue // unreachable; already reported
				}
				fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s/%s\t%d/%d\t%d\t%d\t%s\n",
					shortServer(server),
					fmtVersion(c),
					c.FreeCPU, c.TotalCPU,
					fmtMemMB(int64(c.FreeRAMMB)), fmtMemMB(int64(c.TotalRAMMB)),
					c.FreeGPU, c.TotalGPU,
					c.ActiveJobs, c.QueueLength,
					joinCaps(c.Capabilities))
			}
			tw.Flush()
			return nil
		},
	}
}

// taggedJob is one job plus the runner that reported it: the fan-out
// helpers key results by server, but cancel needs both in one value.
type taggedJob struct {
	job    *api.JobStatus
	server string
}

// cancellableJobs returns the active + queued jobs across the given
// servers, each tagged with its originating server.
func cancellableJobs(servers []string) []taggedJob {
	var jobs []taggedJob

	active, errs := runclient.ActiveAll(servers)
	reportErrors(errs)
	for server, list := range active {
		for _, j := range list {
			jobs = append(jobs, taggedJob{job: j, server: server})
		}
	}

	queued, errs := runclient.QueueAll(servers)
	reportErrors(errs)
	for server, list := range queued {
		for _, j := range list {
			jobs = append(jobs, taggedJob{job: j, server: server})
		}
	}
	return jobs
}

// findJob locates a specific job by ID among the cancellable jobs so we
// can resolve its handler image (needed to look up the DeployKey) and its
// server.
func findJob(servers []string, jobID string) (taggedJob, error) {
	for _, t := range cancellableJobs(servers) {
		if t.job.JobID == jobID {
			return t, nil
		}
	}
	return taggedJob{}, fmt.Errorf("job %s not found among active/queued jobs on %v", jobID, servers)
}

// pickJob presents an interactive chooser over active + queued jobs.
func pickJob(servers []string) (taggedJob, error) {
	jobs := cancellableJobs(servers)
	if len(jobs) == 0 {
		return taggedJob{}, fmt.Errorf("no active or queued jobs to cancel")
	}

	labels := make([]string, len(jobs))
	for i, t := range jobs {
		labels[i] = fmt.Sprintf("[%s] %s  %s  user=%s  %s", t.job.Status, t.job.Handler, t.job.JobID, t.job.User, shortServer(t.server))
	}
	idx, err := pickFrom("Select job to cancel:", labels)
	if err != nil {
		return taggedJob{}, err
	}
	return jobs[idx], nil
}

func cmdCancel() *cli.Command {
	return &cli.Command{
		Name:    "cancel",
		Aliases: []string{"x"},
		Usage:   "tie-run cancel [jobID] [--servers a,b]",
		Description: "Cancel a queued or running job. With no jobID, pick interactively from active and queued jobs. " +
			"Requires the job image's DeployKey (from " + projectFile + " or tie-run-config.toml).",
		Flags: []cli.Flag{
			serversFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}

			var target taggedJob
			if jobID := cmd.Args().First(); jobID != "" {
				job, err := findJob(srv, jobID)
				if err != nil {
					return err
				}
				target = job
			} else {
				job, err := pickJob(srv)
				if err != nil {
					return err
				}
				target = job
			}

			deployKey, err := deployKeyFor(target.job.Handler)
			if err != nil {
				return err
			}

			runner := target.server
			if runner == "" {
				runner = target.job.DeployManager
			}
			checkAuthScheme(runner)
			if err := runclient.CancelJob(runner, target.job.JobID, deployKey); err != nil {
				return err
			}
			fmt.Printf("✓ Cancelled job %s (%s) on %s\n", target.job.JobID, target.job.Handler, shortServer(runner))
			return nil
		},
	}
}

// cmdServices lists deployed services across the fleet; the status
// filter is applied case-insensitively.
func cmdServices() *cli.Command {
	return &cli.Command{
		Name:        "services",
		Aliases:     []string{"svc"},
		Usage:       "tie-run services [--status running] [--servers a,b] [--json]",
		Description: "List deployed services across all runners. Use the start/stop subcommands to control a service.",
		Flags: []cli.Flag{
			serversFlag(),
			&cli.StringFlag{Name: "status", Usage: "Filter by status: running|stopped|crashed"},
			jsonFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}

			services, errs := runclient.ListServicesAll(srv)
			reportErrors(errs)

			status := strings.TrimSpace(strings.ToLower(cmd.String("status")))
			if status != "" {
				var filtered []runclient.Keyed[api.TaskInfo]
				for _, s := range services {
					if strings.ToLower(s.Value.Status) == status {
						filtered = append(filtered, s)
					}
				}
				services = filtered
			}

			if cmd.Bool("json") {
				return printJSON(services)
			}

			if len(services) == 0 {
				fmt.Println("No services found.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tNAME\tTYPE\tSTATUS\tPORT")
			for _, s := range services {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					shortServer(s.Server), s.Value.Name, s.Value.TaskType, s.Value.Status, fmtPort(s.Value.ExternalPort))
			}
			tw.Flush()
			return nil
		},
		Commands: []*cli.Command{
			serviceControlCmd("start", true),
			serviceControlCmd("stop", false),
		},
	}
}

// serviceControlCmd builds the start/stop subcommand. Both act on the
// same runner resource, dispatched by HTTP method (POST=start,
// DELETE=stop). The target runner and DeployKey come from the project
// file in the current directory or tie-run-config.toml, which pins each
// service to the exact host it was deployed to — avoiding ambiguity when
// the same image name is deployed on several runners.
func serviceControlCmd(name string, start bool) *cli.Command {
	return &cli.Command{
		Name:        name,
		Usage:       fmt.Sprintf("tie-run services %s <service-name>", name),
		Description: fmt.Sprintf("%s%s a service by name. Requires the service image's runner and DeployKey (from %s in the current directory, or tie-run-config.toml).", strings.ToUpper(name[:1]), name[1:], projectFile),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			svcName := cmd.Args().First()
			if svcName == "" {
				return fmt.Errorf("service name is required (e.g. `tie-run services %s postgres:latest`)", name)
			}

			// Same precedence as delete/cancel: project file in the
			// current directory first, then tie-run-config.toml. No
			// prompt: service control is expected to run from scripts too.
			target, err := lookupTarget(svcName, false)
			if err != nil {
				return err
			}

			checkAuthScheme(target.Runner)
			if err := runclient.SetService(target.Runner, svcName, target.DeployKey, start); err != nil {
				return err
			}
			verb := "started"
			if !start {
				verb = "stopped"
			}
			fmt.Printf("✓ %s service %s on %s\n", verb, svcName, shortServer(target.Runner))
			return nil
		},
	}
}

func cmdDelete() *cli.Command {
	return &cli.Command{
		Name:        "delete",
		Aliases:     []string{"del", "rm"},
		Usage:       "tie-run delete <image-name>",
		Description: "Delete a deployed image from the runner. Reads DeployKey from " + projectFile + " in PWD if available, otherwise from tie-run-config.toml, or prompts if not found.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			imageName := cmd.Args().First()
			if imageName == "" {
				return fmt.Errorf("image name is required (e.g. `tie-run delete myapp:latest`)")
			}

			// Project file -> tie-run-config.toml -> interactive prompt,
			// the same precedence every management command uses.
			target, err := lookupTarget(imageName, true)
			if err != nil {
				return err
			}
			runner, deployKey := target.Runner, target.DeployKey

			// Confirm deletion
			if !termIsTerminal() {
				return fmt.Errorf("deleting %q needs interactive confirmation, but stdin is not a terminal", imageName)
			}
			fmt.Printf("⚠️  About to delete image '%s' from %s\n", imageName, shortServer(runner))
			fmt.Print("This will stop the service (if running), remove the container image, and delete the config entry.\nType the image name to confirm: ")
			confirmation, err := readLine()
			if err != nil {
				return err
			}
			if strings.TrimSpace(confirmation) != imageName {
				return fmt.Errorf("deletion cancelled (confirmation did not match)")
			}

			checkAuthScheme(runner)
			if err := runclient.DeleteImage(runner, imageName, deployKey); err != nil {
				return err
			}

			fmt.Printf("✓ Deleted image %s from %s\n", imageName, shortServer(runner))
			return nil
		},
	}
}

func cmdVersion() *cli.Command {
	return &cli.Command{
		Name:    "version",
		Aliases: []string{"v"},
		Usage:   "Show version information",
		Flags: []cli.Flag{
			serversFlag(),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Printf("tie-run version %s\n", version.String())

			// Show the whole fleet, not just the seed runner, reusing the
			// capacity command's version column.
			srv := parseServers(cmd.String("servers"))
			if err := requireServers(srv); err != nil {
				return err
			}
			caps, errs := runclient.CapabilitiesAll(srv)
			for _, server := range srv {
				if err, bad := errs[server]; bad {
					fmt.Printf("\nServer (%s): unreachable: %s\n", server, err.Error())
					continue
				}
				c := caps[server]
				if c.Version == "" {
					fmt.Printf("\nServer (%s): version not reported (older runner)\n", server)
					continue
				}
				fmt.Printf("\nServer (%s): %s\n", server, fmtVersion(c))
			}
			return nil
		},
	}
}
