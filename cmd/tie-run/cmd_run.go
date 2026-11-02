package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/uidbz/tie/runclient"
	"github.com/uidbz/tie/runner/api"

	"github.com/urfave/cli/v3"
)

func cmdRun() *cli.Command {
	return &cli.Command{
		Name:    "run",
		Aliases: []string{"r"},
		Usage:   "tie-run run image input-dir output-dir",
		Description: "Start a container job with arbitrary input. Image is the runner-deployed image name. " +
			"An image list joined with '|' (a|b|c) is a client-side chain: tie-run runs each step in order, feeding each " +
			"output into the next, and stops if the shell is interrupted. A real pipeline (runclient.SubmitPipeline in a " +
			"script) is submitted to the runner, which sequences and tracks the steps itself and can be controlled with " +
			"tie-run pipeline list/show/cancel/resume.",
		Flags: []cli.Flag{
			runnerFlag(),
			&cli.StringFlag{Name: "jobtype", Value: "CPUv1"},
			&cli.StringFlag{Name: "settings", Usage: "Settings JSON file, passed to the job verbatim"},
			&cli.StringFlag{Name: "app", Usage: "Local dir to upload as the injected app"},
		},

		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 3 {
				return errors.New("tie-run run needs exactly 3 arguments: tie-run run container-name input-dir output-dir")
			}
			runner, err := resolvedRunner(cmd)
			if err != nil {
				return err
			}
			if debugMode {
				fmt.Println("!! Debug mode activated - messaging more verbose !!")
			}
			handler := cmd.Args().Get(0)
			input := cmd.Args().Get(1)
			output := cmd.Args().Get(2)
			jobtype := cmd.String("jobtype")
			injectedApp := cmd.String("app")
			filehost, store, err := runclient.GetFilehostStore(runner)
			if debugMode {
				fmt.Println("Image:", handler)
				fmt.Println("Input:", input)
				fmt.Println("Output:", output)
				fmt.Println("Filehost:", filehost)
				fmt.Println("Runner:", runner)
				fmt.Println("JobType:", jobtype)
				fmt.Println("Injected App:", injectedApp)
			}

			if err != nil {
				return errors.New("Deploy manager error: " + err.Error())
			}

			inputHash, err := runclient.Upload(filehost, input, store)
			if err != nil {
				return err
			}

			var appHash string
			if len(injectedApp) != 0 {
				appHash, err = runclient.Upload(filehost, injectedApp, store)
				if err != nil {
					return err
				}
			}

			// Read the settings file once, not per pipeline stage.
			var settings json.RawMessage
			if settingsFile := cmd.String("settings"); settingsFile != "" {
				b, err := os.ReadFile(settingsFile)
				if err != nil {
					return err
				}
				settings = b
			}

			pipeline := strings.Split(handler, "|")

			for i, currentHandler := range pipeline {
				req := api.JobRequest{
					JobType:   jobtype,
					Handler:   currentHandler,
					InputHash: inputHash,
					AppHash:   appHash,
					Settings:  settings,
					Debug:     debugMode,
				}
				// StartRunCtx threads the app context so Ctrl-C stops the
				// watch with a resume hint. Use the normalised runner, not
				// the raw flag value.
				status, err := runclient.StartRunCtx(ctx, runner, false, req)
				if err != nil {
					return err
				}

				outputDir := output
				if len(pipeline) > 1 {
					before, _, _ := strings.Cut(currentHandler, ":")
					outputDir = filepath.Join(outputDir, strconv.Itoa(i)+". "+before)
				}

				if err := runclient.Download(filehost, status.OutputHash, outputDir); err != nil {
					return err
				}

				inputHash = status.OutputHash
			}

			return nil
		},
	}
}
