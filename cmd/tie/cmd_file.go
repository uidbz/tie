package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
)

// resolveFileHost picks the filehost for a file command. A raw --server address
// takes precedence (with --insecure controlling TLS verification); otherwise the
// named --host is looked up in config.
func (a *app) resolveFileHost(ctx *cli.Command) (client.FileHost, error) {
	if server := ctx.String("server"); server != "" {
		return client.FileHost{URL: server, Insecure: ctx.Bool("insecure")}, nil
	}
	return a.tie.ResolveHost(ctx.String("host"))
}

// dirTreeSize totals the bytes of path, recursing into directories. It returns
// 0 when path cannot be stat-ed, letting the progress bar fall back gracefully.
func dirTreeSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func cmdUpload(a *app) *cli.Command {
	return &cli.Command{
		Name:  "upload",
		Usage: "Upload a file or directory to a filehost: upload [file]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first of the collection's FileHosts)"},
			&cli.StringFlag{Name: "server", Usage: "Raw filehost URL, bypassing config"},
			&cli.BoolFlag{Name: "insecure", Usage: "Skip TLS certificate verification (with --server)"},
			&cli.BoolFlag{Name: "json", Usage: "Emit result as JSON"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if ctx.Args().Len() < 1 {
				return errors.New("Need 1 arg: file")
			}
			host, err := a.resolveFileHost(ctx)
			if err != nil {
				return err
			}
			path := ctx.Args().First()
			bar := progressbar.NewOptions64(
				dirTreeSize(path),
				progressbar.OptionSetDescription("Uploading"),
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionShowBytes(true),
				progressbar.OptionShowCount(),
				progressbar.OptionClearOnFinish(),
				// Without a throttle the bar re-renders the terminal line on
				// every write it receives — one per streamed chunk — which for a
				// multi-GB upload is millions of renders and throttles transfer
				// to a few MB/s. Cap redraws to ~60fps; transfer stays wire-speed.
				progressbar.OptionThrottle(16*time.Millisecond),
			)
			result, err := client.UploadToWithProgress(host, path, bar)
			bar.Finish()
			if err != nil {
				return err
			}
			if result.ErrorMsg != "" && len(result.Items) == 0 {
				return errors.New(result.ErrorMsg)
			}
			format, err := outputFormat(ctx)
			if err != nil {
				return err
			}
			if ctx.Bool("json") || format == "json" {
				out, err := json.Marshal(result)
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
			o, err := a.outFor(ctx, "hash", "filename")
			if err != nil {
				return err
			}
			for _, item := range result.Items {
				o.row(item.Hash, item.Filename)
			}
			return o.flush()
		},
	}
}

func cmdDownload(a *app) *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "Download a file or directory from a filehost: download SOURCE-HASH DEST (- reads hashes from stdin)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first of the collection's FileHosts)"},
			&cli.StringFlag{Name: "server", Usage: "Raw filehost URL, bypassing config"},
			&cli.BoolFlag{Name: "insecure", Usage: "Skip TLS certificate verification (with --server)"},
			stdinFlag(),
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if ctx.Args().Len() < 2 {
				return exitError{code: exitUsage, msg: "download takes SOURCE-HASH DEST (or --stdin to read hashes from stdin; a lone - cannot be followed by DEST)"}
			}
			host, err := a.resolveFileHost(ctx)
			if err != nil {
				return err
			}
			hashes := []string{ctx.Args().Get(0)}
			if wantsStdin(ctx) {
				// Several hashes land in DEST, so DEST has to be a directory;
				// saying so beats writing each download over the last (§6.7).
				hashes, err = readStdinKeys()
				if err != nil {
					return err
				}
				if len(hashes) == 0 {
					fmt.Fprintln(os.Stderr, "no hashes on stdin")
					return nil
				}
				if len(hashes) > 1 {
					if fi, err := os.Stat(ctx.Args().Get(1)); err != nil || !fi.IsDir() {
						return exitError{code: exitUsage, msg: "downloading several hashes needs DEST to be an existing directory"}
					}
				}
			}
			for _, sourceHash := range hashes {
				if err := downloadOne(a, host, sourceHash, ctx); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// downloadOne fetches one content hash to the destination named on the command
// line, with the progress bar on stderr.
func downloadOne(a *app, host client.FileHost, sourceHash string, ctx *cli.Command) error {
	total, err := client.DownloadSize(host, sourceHash)
	if err != nil {
		return err
	}
	bar := progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription("Downloading"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(),
		// See the upload bar: cap redraws so per-chunk writes don't
		// throttle the transfer to terminal-render speed.
		progressbar.OptionThrottle(16*time.Millisecond),
	)
	err = client.DownloadFromWithProgress(host, sourceHash, ctx.Args().Get(1), bar)
	bar.Finish()
	return err
}
