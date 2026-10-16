package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
)

// cmdTag groups tag-management subcommands. Tags attach to a content hash, so
// del/rename act on the tag everywhere that content appears in the current
// collection, and everywhere in the tag-query views.
func cmdTag(a *app) *cli.Command {
	return &cli.Command{
		Name:  "tag",
		Usage: "Manage tags: list, add, del, rename, files, show, set",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List all known tag names: tag list",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0},
					&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 0, Usage: "Max tags to list (0 = all)"},
				},
				Action: func(_ context.Context, ctx *cli.Command) error {
					tags, _, err := a.tie.ListTags(ctx.Int("offset"), ctx.Int("limit"))
					if err != nil {
						return errors.New("Tag list Error: " + err.Error())
					}
					for _, t := range tags {
						fmt.Println(t)
					}
					return nil
				},
			},
			{
				Name:  "add",
				Usage: "Register a tag name in the store (no file needed): tag add [tag]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					if err := a.tie.RegisterTag(ctx.Args().First()); err != nil {
						return errors.New("Tag add Error: " + err.Error())
					}
					return nil
				},
			},
			{
				Name:  "del",
				Usage: "Delete a tag from every item and the registry: tag del [tag]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					n, err := a.tie.DeleteTag(ctx.Args().First())
					if err != nil {
						return errors.New("Tag del Error: " + err.Error())
					}
					fmt.Fprintf(os.Stderr, "Removed tag from %d item(s)\n", n)
					return nil
				},
			},
			{
				Name:  "rename",
				Usage: "Rename a tag everywhere it is used: tag rename [tag] [newname]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 2 {
						return errors.New("Need 2 args: tag, newname")
					}
					n, err := a.tie.RenameTag(ctx.Args().Get(0), ctx.Args().Get(1))
					if err != nil {
						return errors.New("Tag rename Error: " + err.Error())
					}
					fmt.Fprintf(os.Stderr, "Renamed tag on %d item(s)\n", n)
					return nil
				},
			},
			{
				Name:  "files",
				Usage: "List items carrying a tag: tag files [tag]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					files, _, err := a.tie.FilesWithTags("", []string{ctx.Args().First()}, nil, 0, -1)
					if err != nil {
						return errors.New("Tag files Error: " + err.Error())
					}
					for _, f := range files {
						fmt.Println(f.Hash + "\t" + f.Filename)
					}
					return nil
				},
			},
			{
				Name:  "untagged",
				Usage: "List files/dirs that carry no tag: tag untagged [--type tie-type]",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "type", Aliases: []string{"t"}, Usage: "Restrict to one tie-type (e.g. audio-file, image-dir); default: all files and directories"},
					&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0},
					&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 0, Usage: "Max items to list (0 = all)"},
				},
				Action: func(_ context.Context, ctx *cli.Command) error {
					files, _, err := a.tie.UntaggedFiles(ctx.String("type"), ctx.Int("offset"), ctx.Int("limit"))
					if err != nil {
						return errors.New("Tag untagged Error: " + err.Error())
					}
					for _, f := range files {
						fmt.Println(f.Hash + "\t" + f.Filename)
					}
					return nil
				},
			},
			{
				Name:  "show",
				Usage: "Show the tags on a content hash: tag show [hash]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: hash")
					}
					tags, err := client.GetTags(a.tie, ctx.Args().First())
					if err != nil {
						return errors.New("Tag show Error: " + err.Error())
					}
					for _, t := range tags {
						fmt.Println(t)
					}
					return nil
				},
			},
			{
				Name:  "set",
				Usage: "Replace the full tag set on a content hash: tag set [hash] [tags...]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need at least 1 arg: hash (pass no tags to clear)")
					}
					hash := ctx.Args().First()
					tags := ctx.Args().Tail()
					if err := client.SetTags(a.tie, hash, tags); err != nil {
						return errors.New("Tag set Error: " + err.Error())
					}
					return nil
				},
			},
		},
	}
}

// cmdVersions groups file version-history subcommands. Superseded content lives
// in the isolated "<Collection>_prev" history collection; these commands list it
// and restore a prior version as the live file. (The top-level `restore` command
// is the TSV-backup restore, so version restore lives under this group.)
func cmdVersions(a *app) *cli.Command {
	return &cli.Command{
		Name:  "versions",
		Usage: "Inspect and restore file version history: versions list, restore",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the superseded versions of a file (newest first): versions list [path]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: path")
					}
					versions, err := a.tie.ListVersions(ctx.Args().First())
					if err != nil {
						return errors.New("Versions list Error: " + err.Error())
					}
					for _, v := range versions {
						fmt.Printf("%s\t%s\t%d\t%s\n", v.Hash, v.Date.Format(time.RFC3339), v.Size, v.Filename)
					}
					return nil
				},
			},
			{
				Name:  "restore",
				Usage: "Restore a prior version as the live file (defaults to newest): versions restore [path] [hash]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if ctx.Args().Len() < 1 {
						return errors.New("Need at least 1 arg: path (optional 2nd arg: version hash)")
					}
					hash, err := a.tie.RestoreVersion(ctx.Args().Get(0), ctx.Args().Get(1))
					if err != nil {
						return errors.New("Versions restore Error: " + err.Error())
					}
					fmt.Println(hash)
					return nil
				},
			},
		},
	}
}

// importFlags are shared by the bare `import` command and every dir-type
// subcommand.
