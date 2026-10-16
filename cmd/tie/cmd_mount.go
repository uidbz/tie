package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/io/fuselib"
	"github.com/urfave/cli/v3"
)

func cmdMount(a *app) *cli.Command {
	return &cli.Command{
		Name: "mount", Usage: "Mount a content-addressed directory (mount [dir-hash] [mountpoint]) " +
			"or the live tag-derived filesystem (mount --db [mountpoint])",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first of the collection's FileHosts)"},
			&cli.IntFlag{Name: "cache", Usage: "On-disk file cache size in GB", Value: 1},
			&cli.BoolFlag{Name: "db", Usage: "Mount the live tag-derived filesystem instead of a dir-hash"},
			&cli.BoolFlag{Name: "verify", Usage: "Verify downloaded bytes against their content hash (default off; use for an untrusted filehost)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {

			filehost, err := a.tie.ResolveHost(ctx.String("host"))
			if err != nil {
				return err
			}

			var server *fuse.Server
			var what, mountpoint string
			if ctx.Bool("db") {
				if ctx.Args().Len() < 1 {
					return errors.New("Need 1 arg: mountpoint")
				}
				mountpoint = ctx.Args().Get(0)
				state := fuselib.NewTieDBFuse(a.tie, filehost, ctx.Int("cache"), ctx.Bool("verify"))
				defer state.Close()
				s, err := state.MountDB(mountpoint)
				if err != nil {
					return err
				}
				server, what = s, "tag-derived filesystem"
			} else {
				if ctx.Args().Len() < 2 {
					return errors.New("Need 2 args: dir-hash, mountpoint")
				}
				hash := ctx.Args().Get(0)
				mountpoint = ctx.Args().Get(1)
				state := fuselib.NewTieFuse(filehost, ctx.Int("cache"), ctx.Bool("verify"))
				defer state.Close()
				s, err := state.Mount(hash, mountpoint)
				if err != nil {
					return err
				}
				server, what = s, hash
			}

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sig
				fmt.Fprintln(os.Stderr, "\nUnmounting", mountpoint)
				if err := server.Unmount(); err != nil {
					fmt.Fprintln(os.Stderr, "Unmount error:", err.Error())
				}
			}()

			fmt.Fprintf(os.Stderr, "Mounted %s at %s (Ctrl-C to unmount)\n", what, mountpoint)
			server.Wait()

			return nil
		},
	}
}
