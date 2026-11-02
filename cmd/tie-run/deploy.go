package main

// Deploy builds a project on the runner: upload the source tree (plus
// test data for a test deploy), POST the frozen deploy request, persist
// the rotated DeployKey, optionally restart the service, and for test
// deploys watch the test job and download its output.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"

	"github.com/urfave/cli/v3"
)

// resolveDeployWorkdir resolves workdir to an absolute path and makes it
// the process CWD, so tie-project.toml and every relative path in it
// resolve against the project rather than the caller's CWD. Deploy is
// single-threaded, so changing the process CWD here is safe.
func resolveDeployWorkdir(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	if err := os.Chdir(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// deployOpts is everything the deploy steps need, assembled by the cli
// action from flags and args. Keeping it explicit removes the closure's
// reads from globals and makes the steps testable.
type deployOpts struct {
	Workdir string // project dir; "." when no argument was given
	Prod    bool
	Restart bool // applies only to --prod (decideRestart)
	NoCache bool
	Debug   bool // the global --debug flag at action time
}

// decideRestart pins the decision: --restart only applies to prod
// deploys. On a test deploy the flag is ignored (with a notice printed by
// deploy), because restarting the service before the test job has even
// run would restart the old image.
func decideRestart(opts deployOpts) bool {
	return opts.Restart && opts.Prod
}

// deployProject is a loaded project plus the absolute workdir it was
// loaded from. loadProject also made that workdir the process CWD, so
// relative paths in the project file resolve against the project;
// Workdir is kept so filepath.Join stays correct even while putlib
// temporarily Chdirs during an upload.
type deployProject struct {
	TieProject
	Workdir string
}

// deploy runs the deploy as named steps: load, upload, submit, persist
// the key, optionally restart, and for test deploys watch and download
// the test job.
func deploy(opts deployOpts) error {
	p, err := loadProject(opts.Workdir)
	if err != nil {
		return err
	}
	filehost, store, err := runclient.GetFilehostStore(p.Runner)
	if err != nil {
		return errors.New("Deploy manager error: " + err.Error())
	}

	// Either the project file or the --debug flag turns debug on for the
	// whole run.
	if p.Debug || opts.Debug {
		p.Debug = true
		debugMode = true
	}
	if debugMode {
		fmt.Println("!! Debug mode activated - messaging more verbose !!")
		fmt.Println("Received file server:", filehost)
	}

	req, err := uploadProject(p, filehost, store, opts)
	if err != nil {
		return err
	}

	prodstring := "(TEST)"
	if opts.Prod {
		prodstring = "(PRODUCTION)"
	}
	fmt.Println(prodstring + " Deploying... Please wait")

	resp, err := submitDeploy(req, p.Runner, opts.Prod)
	if err != nil {
		// CallRunner already carries a truncated body in its errors; only
		// dump the build-log message when the user asked for debug.
		if debugMode {
			fmt.Println(resp.Message + "\n")
		}
		return err
	}

	if resp.ServiceExternalPort > 0 {
		fmt.Printf("TCP service reachable on port %d\n", resp.ServiceExternalPort)
	}

	if err := persistDeployKey(p, resp.DeployKey); err != nil {
		return err
	}

	if decideRestart(opts) {
		if err := restart(p); err != nil {
			return err
		}
	} else if opts.Restart {
		fmt.Println("restart ignored for test deploys (use --prod)")
	}

	if !opts.Prod { // test deploy: watch the test job and fetch its output
		if err := runTest(p, resp, filehost); err != nil {
			return err
		}
	}
	fmt.Println("Finished!")
	return nil
}

// loadProject resolves the workdir (one place that knows the path),
// Chdirs into it, and loads tie-project.toml from there.
func loadProject(workdir string) (deployProject, error) {
	var p deployProject
	abs, err := resolveDeployWorkdir(workdir)
	if err != nil {
		return p, errors.New("Workdir error: " + err.Error())
	}
	p.Workdir = abs
	proj, err := loadTieProject(filepath.Join(abs, projectFile))
	if err != nil {
		return p, errors.New("Config error: " + err.Error())
	}
	p.TieProject = proj
	return p, nil
}

// uploadProject uploads the source tree — plus test input, inject app,
// and settings for a test deploy — and returns the deploy request with
// the resulting hashes filled in.
func uploadProject(p deployProject, filehost, store string, opts deployOpts) (api.DeployRequest, error) {
	req := p.deployRequest()
	req.NoCache = opts.NoCache

	fmt.Print("Uploading files...")
	srcPath := filepath.Join(p.Workdir, p.Source)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		fmt.Println("")
		return req, err
	}
	uploadStatus := putlib.Upload(filehost, srcPath, putlib.PutConfig{Store: store})
	if uploadStatus.ErrorMsg != "" {
		return req, errors.New("Upload error: " + uploadStatus.ErrorMsg)
	}
	req.ProjectHash = uploadStatus.LastItem.Hash

	if !opts.Prod {
		// Upload test data
		status2 := putlib.Upload(filehost, filepath.Join(p.Workdir, p.TestInput), putlib.PutConfig{Store: store})
		if status2.ErrorMsg != "" {
			return req, errors.New("\nUpload error: " + status2.ErrorMsg)
		}
		req.TestInputHash = status2.LastItem.Hash

		// Upload app to inject
		if len(p.TestApp) != 0 {
			status2 = putlib.Upload(filehost, filepath.Join(p.Workdir, p.TestApp), putlib.PutConfig{Store: store})
			if status2.ErrorMsg != "" {
				return req, errors.New("\nUpload error: " + status2.ErrorMsg)
			}
			req.AppHash = status2.LastItem.Hash
		}
	}
	fmt.Println(" Done!")
	if debugMode && !opts.Prod {
		fmt.Println("Testdata hash:", req.TestInputHash)
	}

	if p.TestSettings != "" {
		b, err := os.ReadFile(filepath.Join(p.Workdir, p.TestSettings))
		if err != nil {
			return req, err
		}
		req.TestSettingsData = b
	}
	return req, nil
}

// submitDeploy POSTs the deploy request to the runner. The build happens
// on the runner, so it gets a long explicit budget instead of the
// default per-call timeout. CallRunner already wraps non-2xx replies and
// JSON parse failures with a truncated body.
func submitDeploy(req api.DeployRequest, runner string, isProd bool) (api.DeployResponse, error) {
	if isProd {
		req.Command = "prod"
	} else {
		req.Command = "test"
	}

	var r api.DeployResponse
	err := runclient.CallRunner("POST", runner, "/api/deploymanager", req, &r, 30*time.Minute)
	if err != nil {
		return r, err
	}
	if r.ErrorMsg != "" {
		return r, errors.New(r.ErrorMsg)
	}
	return r, nil
}

// persistDeployKey stores a rotated DeployKey from the deploy reply: a
// targeted line edit of tie-project.toml plus a mirror into
// tie-run-config.toml so management commands work right after deploy.
// Never prints the key itself.
func persistDeployKey(p deployProject, newKey string) error {
	if newKey == "" || p.DeployKey == newKey {
		return nil
	}
	path := filepath.Join(p.Workdir, projectFile)
	if err := persistDeployKeyLine(path, newKey); err != nil {
		return fmt.Errorf("failed to update DeployKey in %s: %w", projectFile, err)
	}
	if err := mirrorDeployKey(p.Name, p.Runner, newKey); err != nil {
		return fmt.Errorf("failed to mirror DeployKey to tie-run-config.toml: %w", err)
	}
	if debugMode {
		fmt.Println("Updated DeployKey in", projectFile, "and tie-run-config.toml")
	}
	return nil
}

// restart stops then starts the same control-plane resource: SetService
// already speaks this wire format. A failed stop is only a warning — the
// service may not have been running — but a failed start is the deploy
// failing.
func restart(p deployProject) error {
	fmt.Println("Deploy finished! Restarting service...")
	if err := runclient.SetService(p.Runner, p.Name, p.DeployKey, false); err != nil {
		fmt.Println("Warning: Stop request failed: " + err.Error())
	}
	if err := runclient.SetService(p.Runner, p.Name, p.DeployKey, true); err != nil {
		return fmt.Errorf("restart failed: %w", err)
	}
	fmt.Println("Service restarted!")
	return nil
}

// runTest watches the test job the deploy started and downloads its
// output into the project's TestOutput dir.
func runTest(p deployProject, resp api.DeployResponse, filehost string) error {
	status, err := runclient.ShowProgress(p.Runner, resp.JobStatus.JobID, false, true)
	if err != nil {
		return err
	}
	if debugMode {
		fmt.Println(status.Log + "\n")
	}
	if err := runclient.Download(filehost, status.OutputHash, p.TestOutput); err != nil {
		return errors.New("Error downloading test output: " + err.Error())
	}
	if out, err := filepath.Abs(p.TestOutput); err == nil {
		fmt.Println("Test output:", out)
	}
	return nil
}

func cmdDeploy() *cli.Command {
	return &cli.Command{
		Name:        "deploy",
		Aliases:     []string{"d"},
		Usage:       "tie-run deploy [--prod] [workdir]",
		Description: "Build a project on the runner and, with --prod, publish it as a deployed image. Reads " + projectFile + " from the workdir.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "prod"},
			&cli.BoolFlag{Name: "nocache"},
			&cli.BoolFlag{Name: "restart"},
		},

		// Thin adapter: turn flags/args into deployOpts and run the steps.
		// All logic lives in deploy() and its named steps.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			opts, err := deployOptsFromArgs(cmd.Args(), cmd.Bool("prod"), cmd.Bool("restart"), cmd.Bool("nocache"), debugMode)
			if err != nil {
				return err
			}
			return deploy(opts)
		}}
}

// deployOptsFromArgs assembles deployOpts from the command's flags and
// positional args: 0 or 1 workdir arguments, cleaned but not yet made
// absolute (loadProject owns that).
func deployOptsFromArgs(args cli.Args, prod, restart, noCache, debug bool) (deployOpts, error) {
	var opts deployOpts
	if args.Len() > 1 {
		return opts, errors.New("Too many arguments given, expected 0 or 1, got " + strconv.Itoa(args.Len()))
	}
	opts.Workdir = "."
	if args.Len() == 1 {
		opts.Workdir = filepath.Clean(args.First())
	}
	opts.Prod = prod
	opts.Restart = restart
	opts.NoCache = noCache
	opts.Debug = debug
	return opts, nil
}
