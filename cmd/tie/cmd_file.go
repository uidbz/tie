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
			if ctx.Bool("json") {
				out, err := json.Marshal(result)
				if err != nil {
					return err
				}
				fmt.Println(string(out))
			} else {
				for _, item := range result.Items {
					fmt.Println(item.Hash + "\t" + item.Filename)
				}
			}
			return nil
		},
	}
}

func cmdDownload(a *app) *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "Download a file or directory from a filehost: download [source-hash] [dest]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first of the collection's FileHosts)"},
			&cli.StringFlag{Name: "server", Usage: "Raw filehost URL, bypassing config"},
			&cli.BoolFlag{Name: "insecure", Usage: "Skip TLS certificate verification (with --server)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if ctx.Args().Len() < 2 {
				return errors.New("Need 2 args: source-hash, dest")
			}
			host, err := a.resolveFileHost(ctx)
			if err != nil {
				return err
			}
			sourceHash := ctx.Args().Get(0)
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
		},
	}
}
