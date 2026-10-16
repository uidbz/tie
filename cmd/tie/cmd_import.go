package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
)

// importFlags are shared by the bare `import` command and every dir-type
// subcommand.
func importFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "collection", Usage: "Collection to tag into (default: config Collection)"},
		&cli.StringSliceFlag{Name: "tags", Aliases: []string{"t"}},
		&cli.StringSliceFlag{Name: "host", Usage: "Filehost name(s) from config; repeatable to mirror (default: the collection's FileHosts)"},
		&cli.StringFlag{Name: "dest", Usage: "Virtual path to root the imported directory tree at (overrides config ImportDest and the source path). With --albums: inline destination template (e.g. \"/music/{albumartist}/{year}. {album}\")"},
		&cli.BoolFlag{Name: "albums", Usage: "Bulk-import a library root: discover albums (directories / tag clusters) and import each as its own labeled root"},
		&cli.StringFlag{Name: "by", Usage: "Album grouping for --albums: auto (dirs, merged/split by tags), dir (one directory = one album), tags (cluster by album tags)", Value: "auto"},
		&cli.BoolFlag{Name: "dry-run", Usage: "With --albums: print the discovered albums and destinations without importing"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "With --albums: import without the confirmation prompt"},
	}
}

// builtinDirTypes are the dir-type labels always available as import
// subcommands, regardless of config. Custom types are added from the config's
// [ImportDest] keys.
var builtinDirTypes = []string{"audio-dir", "image-dir", "video-dir", "document-dir"}

// builtinArchiveTypes are archive tie-types offered as import subcommands. Unlike
// dir-types they do not label a directory root: an archive is a single file, so
// the subcommand forces the classification of archive files (zips) encountered
// during the import while leaving other files auto-detected.
var builtinArchiveTypes = []string{"image-archive", "audio-archive", "video-archive", "document-archive"}

// runImport uploads and tags every path argument. Each file's own tie-type is
// detected from its contents. label is the invoked subcommand: a dir-type labels
// the root of an imported directory tree; an archive type instead forces the
// tie-type of archive files (zips) found during the import.
func runImport(a *app, ctx *cli.Command, label string) error {
	// stdout carries data: one line per imported item, hash and virtual path,
	// the same shape `tie upload` prints, so `tie import ... | cut -f1` works
	// (§6.1). Everything else the import says goes to stderr or the logger.
	importOut, err := a.outFor(ctx, "hash", "path", "tie-type", "size")
	if err != nil {
		return err
	}
	a.tie.OnImport = func(it client.ImportedItem) {
		importOut.row(it.Hash, it.Path, it.TieType.String(), strconv.Itoa(it.Size))
	}
	dirType := label
	var forcedArchive client.TieType
	if t := client.StringToTieType(label); client.IsArchiveType(t) {
		// Archive subcommand: don't mislabel the directory root; force archive
		// files instead.
		forcedArchive = t
		dirType = client.TieDirectory.String()
	}
	if ctx.Bool("albums") {
		return runImportAlbums(a, ctx, dirType, forcedArchive)
	}
	scoped := a.tie.In(ctx.String("collection"))
	hosts := scoped.ResolveHosts(ctx.StringSlice("host"))
	for _, h := range hosts {
		if _, err := scoped.ResolveHost(h); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "Uploading to", hosts)
	// Keep going past a failed argument/host so one bad path doesn't block the
	// rest, but report every failure on stderr and exit non-zero if any occurred
	// so a scripted/cron import can detect a partial result.
	var failed []string
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "tie: import %s: %v\n", what, err)
		failed = append(failed, what)
	}
	for _, file := range ctx.Args().Slice() {
		fi, err := os.Stat(file)
		if err != nil {
			fail(file, err)
			continue
		}
		for _, h := range hosts {
			if fi.IsDir() {
				if err := scoped.ImportDir(file, a.tie.Config.FileHosts[h], dirType, ctx.StringSlice("tags"), ctx.String("dest"), forcedArchive); err != nil {
					fail(file+" -> "+h, err)
				}
			} else {
				if err := scoped.ImportFile(file, a.tie.Config.FileHosts[h], ctx.StringSlice("tags"), "", forcedArchive); err != nil {
					fail(file+" -> "+h, err)
				}
			}
		}
	}
	if err := importOut.flush(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d import(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// runImportAlbums implements `import <dir-type> --albums <root>...`: discover
// the albums under each root with client.PlanAlbumImport, show the plan, and
// import group by group. --dry-run stops after printing; -y skips the
// confirmation prompt. Keep going past failed roots/groups, reporting all
// failures at the end like runImport.
func runImportAlbums(a *app, ctx *cli.Command, dirType string, forcedArchive client.TieType) error {
	mode, err := client.ParseGroupMode(ctx.String("by"))
	if err != nil {
		return err
	}
	scoped := a.tie.In(ctx.String("collection"))
	hosts := scoped.ResolveHosts(ctx.StringSlice("host"))
	for _, h := range hosts {
		if _, err := scoped.ResolveHost(h); err != nil {
			return err
		}
	}
	var failed []string
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "tie: import %s: %v\n", what, err)
		failed = append(failed, what)
	}
	for _, root := range ctx.Args().Slice() {
		fi, err := os.Stat(root)
		if err != nil {
			fail(root, err)
			continue
		}
		if !fi.IsDir() {
			fail(root, errors.New("not a directory (--albums scans library roots)"))
			continue
		}
		fmt.Fprintln(os.Stderr, "scanning", root, "…")
		plan, err := client.PlanAlbumImport(a.tie.Config, root, client.AlbumPlanOptions{
			DirType:  dirType,
			Template: ctx.String("dest"),
			Mode:     mode,
			ScanProgress: func(scanned, total int) {
				fmt.Fprintf(os.Stderr, "\rprobed %d/%d files", scanned, total)
				if scanned == total {
					fmt.Fprintln(os.Stderr)
				}
			},
		})
		if err != nil {
			fail(root, err)
			continue
		}
		if len(plan) == 0 {
			fmt.Fprintln(os.Stderr, root+": no albums found")
			continue
		}
		printAlbumPlan(plan)
		if ctx.Bool("dry-run") {
			continue
		}
		if !ctx.Bool("yes") {
			yes, err := confirmAlbumImport(len(plan))
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintln(os.Stderr, "aborted")
				continue
			}
		}
		for _, h := range hosts {
			fmt.Fprintln(os.Stderr, "Uploading to", h)
			results := a.tie.ImportAlbums(plan, client.AlbumImportOptions{
				Host:          a.tie.Config.FileHosts[h],
				Collection:    ctx.String("collection"),
				DirType:       dirType,
				Tags:          ctx.StringSlice("tags"),
				ForcedArchive: forcedArchive,
				Progress: func(done, total int, g client.AlbumGroup) {
					fmt.Fprintf(os.Stderr, "[%d/%d] %s (%d tracks)\n", done+1, total, g.Dest, g.Tracks)
				},
			})
			for _, r := range results {
				if r.Err != nil {
					fail(r.Group.Dest+" -> "+h, r.Err)
				}
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d import(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// printAlbumPlan renders the discovered albums as a table for review before
// (or instead of, with --dry-run) importing.
func printAlbumPlan(plan []client.AlbumGroup) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEST\tARTIST\tALBUM\tTRACKS\tSIZE\tWARNINGS")
	for _, g := range plan {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			g.Dest, g.Artist, g.Album, g.Tracks, humanizeBytes(g.Size), strings.Join(g.Warnings, "; "))
	}
	w.Flush()
	fmt.Fprintf(os.Stderr, "%d albums\n", len(plan))
}

// confirmAlbumImport asks before starting a bulk import; anything but y/yes
// aborts. The question goes to stderr and the answer is read from the
// controlling terminal, never from stdin (§6.3): with stdin from a pipe or
// /dev/null — cron, xargs, a script piping something else in — the prompt was
// swallowed and the import aborted silently, and a pipe whose first line began
// with "y" confirmed it by accident. With no terminal to ask on, refuse and say
// how to confirm non-interactively.
func confirmAlbumImport(n int) (bool, error) {
	return confirmAlbumImportOn(n, openTerminal)
}

// openTerminal is the process' controlling terminal. Opening it fails when
// there is none (a cron job, a daemon, `nohup … &`), which is exactly the case
// the prompt must refuse.
func openTerminal() (*os.File, error) {
	return os.Open("/dev/tty")
}

func confirmAlbumImportOn(n int, open func() (*os.File, error)) (bool, error) {
	tty, err := open()
	if err != nil {
		return false, exitError{
			code: 2,
			msg:  fmt.Sprintf("no terminal to confirm on: pass -y to import %d %s without asking", n, plural(n, "album", "albums")),
		}
	}
	defer tty.Close()

	fmt.Fprintf(os.Stderr, "Import %d %s? [y/N] ", n, plural(n, "album", "albums"))
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && answer == "" {
		// Ctrl-D, or a terminal that closed: that is a refusal, not a crash.
		return false, nil
	}
	return confirms(answer), nil
}

// confirms is the whole of the answer grammar: y or yes, any case.
func confirms(answer string) bool {
	answer = strings.TrimSpace(answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

// dirTypeImport builds an `import <name>` subcommand that labels imported
// directory roots with dirType (e.g. `import audio-dir album/`).
func dirTypeImport(a *app, name string) *cli.Command {
	usage := "import files, labeling directory roots as " + name
	if client.IsArchiveType(client.StringToTieType(name)) {
		usage = "import files, forcing archive files (zips) to " + name
	}
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Flags: importFlags(),
		Action: func(_ context.Context, ctx *cli.Command) error {
			return runImport(a, ctx, name)
		},
	}
}

// importDirTypes returns the dir-type subcommand names: the built-ins plus any
// custom types declared as [ImportDest] keys in config, deduped and with the
// built-ins kept first so their order is stable.
func importDirTypes(config client.Config) []string {
	seen := make(map[string]bool, len(builtinDirTypes)+len(builtinArchiveTypes))
	types := make([]string, 0, len(builtinDirTypes)+len(builtinArchiveTypes)+len(config.ImportDest))
	for _, name := range builtinDirTypes {
		seen[name] = true
		types = append(types, name)
	}
	for _, name := range builtinArchiveTypes {
		seen[name] = true
		types = append(types, name)
	}
	for name := range config.ImportDest {
		if !seen[name] {
			seen[name] = true
			types = append(types, name)
		}
	}
	return types
}

func cmdImport(a *app) *cli.Command {
	subcommands := make([]*cli.Command, 0, len(builtinDirTypes)+len(builtinArchiveTypes)+len(a.config.ImportDest))
	for _, name := range importDirTypes(a.config) {
		subcommands = append(subcommands, dirTypeImport(a, name))
	}
	return &cli.Command{
		Name:    "import",
		Aliases: []string{"i"},
		Usage:   "import files to tie-fileserver and tag them",
		Flags:   importFlags(),
		// Bare `import <paths>` auto-detects each file's type; directory roots
		// get the generic directory label. Use a dir-type subcommand to label a
		// directory root as a media collection (audio-dir, image-dir, ..., or any
		// custom type declared in the config's [ImportDest] table).
		Action: func(_ context.Context, ctx *cli.Command) error {
			return runImport(a, ctx, client.TieDirectory.String())
		},
		Commands: subcommands,
	}
}

// cmdStat prints a human-readable summary of a single item, addressed by either
// a VFS path or a 64-hex key (content hash or DirUID). The aggregation lives in
// the client (StatInfo/Stat/StatPath) so the GUI (tie-fm) shares it; this command
// only formats.
