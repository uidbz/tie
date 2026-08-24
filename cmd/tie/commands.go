package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"

	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"

	"git.sr.ht/~uid/conf"

	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/io/fuselib"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/urfave/cli/v3"
)

func cmdAdd() *cli.Command {
	return &cli.Command{
		Name:    "add",
		Aliases: []string{"a"},
		Usage:   "Add a triple to the database: add [key] [value1] [value2]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "gallery-name", Aliases: []string{"g"}},
			&cli.StringFlag{Name: "tags", Aliases: []string{"t"}},
		},

		Action: func(_ context.Context, cCtx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()

			if _, err := tie.Add(args[0], args[1], args[2]); err != nil {
				return errors.New("Add Error: " + err.Error())
			}

			return nil
		},
	}

}

func cmdGet() *cli.Command {
	return &cli.Command{
		Name:    "get",
		Aliases: []string{"g"},
		Usage:   "Get triples: get [key] (or multiple keys for intersection of reverse value. Use -[key] to exclude instead of joining)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "reverse", Aliases: []string{"r"}},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 1000},
			&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0},
			&cli.StringFlag{Name: "sortby", Aliases: []string{"s"}},
			&cli.StringFlag{Name: "filter", Aliases: []string{"f"}},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if ctx.Args().Len() < 1 {
				return errors.New("Need 1 arg: Key")
			}
			args := ctx.Args().Slice()
			var reverse bool = false
			if len(args) > 1 {
				reverse = true
			} else {
				reverse = ctx.Bool("reverse")
			}
			terms := []string{args[0]}
			excluding := make([]string, 0)
			for i := 1; i < len(args); i++ {
				if strings.HasPrefix(args[i], "-") {
					excluding = append(excluding, strings.TrimPrefix(args[i], "-"))
				} else {
					terms = append(terms, args[i])
				}
			}
			rows, _, err := tie.Query(client.QuerySpec{
				Terms:   terms,
				Exclude: excluding,
				Reverse: reverse,
				Filter:  ctx.String("filter"),
				Offset:  ctx.Int("offset"),
				Limit:   ctx.Int("limit"),
				SortBy:  ctx.String("sortby"),
			})
			if err != nil {
				if errors.Is(err, client.ErrNotFound) {
					return nil
				}
				return errors.New("Get Error: " + err.Error())
			}
			printRows(rows)

			return nil
		},
	}
}

// printRows writes query results to stdout, flattening each Row back into
// key<TAB>relation<TAB>value lines. When stdout is an interactive terminal it
// renders aligned, headered columns for readability; when stdout is piped or
// redirected it emits plain tab-separated lines so downstream tools like
// cut/awk/sort keep working unchanged.
func printRows(rows []client.Row) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		for _, r := range rows {
			for relation, values := range r.Attributes {
				for _, v := range values {
					fmt.Println(r.Key + "\t" + relation + "\t" + v)
				}
			}
		}
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE1\tVALUE2")
	for _, r := range rows {
		for relation, values := range r.Attributes {
			for _, v := range values {
				fmt.Fprintln(tw, r.Key+"\t"+relation+"\t"+v)
			}
		}
	}
	tw.Flush()
}

// resolveFileHost picks the filehost for a file command. A raw --server address
// takes precedence (with --insecure controlling TLS verification); otherwise the
// named --host is looked up in config.
func resolveFileHost(ctx *cli.Command) (client.FileHost, error) {
	if server := ctx.String("server"); server != "" {
		return client.FileHost{URL: server, Insecure: ctx.Bool("insecure")}, nil
	}
	if tie == nil {
		return client.FileHost{}, errors.New("Error: Config not loaded")
	}
	return tie.ResolveHost(ctx.String("host"))
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

func cmdUpload() *cli.Command {
	return &cli.Command{
		Name:  "upload",
		Usage: "Upload a file or directory to a filehost: upload [file]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first DefaultFileHosts)"},
			&cli.StringFlag{Name: "server", Usage: "Raw filehost URL, bypassing config"},
			&cli.BoolFlag{Name: "insecure", Usage: "Skip TLS certificate verification (with --server)"},
			&cli.BoolFlag{Name: "json", Usage: "Emit result as JSON"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if ctx.Args().Len() < 1 {
				return errors.New("Need 1 arg: file")
			}
			host, err := resolveFileHost(ctx)
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

func cmdDownload() *cli.Command {
	return &cli.Command{
		Name:  "download",
		Usage: "Download a file or directory from a filehost: download [source-hash] [dest]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first DefaultFileHosts)"},
			&cli.StringFlag{Name: "server", Usage: "Raw filehost URL, bypassing config"},
			&cli.BoolFlag{Name: "insecure", Usage: "Skip TLS certificate verification (with --server)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if ctx.Args().Len() < 2 {
				return errors.New("Need 2 args: source-hash, dest")
			}
			host, err := resolveFileHost(ctx)
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

func cmdDump() *cli.Command {
	return &cli.Command{
		Name:  "dump",
		Usage: "Dump every triple in the current collection as TSV (key<TAB>value1<TAB>value2) to stdout",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   "Read triples directly from a local .tie file instead of the server (offline export; do not use against a file a running daemon has open)",
			},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			bw := bufio.NewWriter(os.Stdout)
			defer bw.Flush()
			w := csv.NewWriter(bw)
			w.Comma = '\t'
			defer w.Flush()

			if path := ctx.String("file"); path != "" {
				if err := dumpLocalFile(path, w); err != nil {
					return errors.New("Dump Error: " + err.Error())
				}
			} else {
				if tie == nil {
					return errors.New("Error: Config not loaded")
				}
				err := tie.DumpStream(func(t tiedb.StringTriple) error {
					return w.Write([]string{t.Key, t.Value1, t.Value2})
				})
				if err != nil {
					return errors.New("Dump Error: " + err.Error())
				}
			}
			w.Flush()
			return w.Error()
		},
	}
}

// dumpLocalFile reads every forward triple straight from an on-disk .tie file,
// bypassing the server, and streams each as a TSV record to w (no in-memory
// buffering of the whole collection). It reflects flushed on-disk state only,
// so it is meant for offline export when no daemon holds the file open.
func dumpLocalFile(path string, w *csv.Writer) error {
	if !strings.HasSuffix(path, ".tie") {
		return fmt.Errorf("not a .tie file: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	name := strings.TrimSuffix(filepath.Base(path), ".tie")
	db := tiedb.NewDB(true)
	db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	col := db.GetCollection(tiedb.CollectionKey{Database: dir, Collection: name})
	var writeErr error
	col.ForEachTriple(func(t tiedb.StringTriple) {
		if writeErr != nil {
			return
		}
		writeErr = w.Write([]string{t.Key, t.Value1, t.Value2})
	})
	return writeErr
}

func cmdRestore() *cli.Command {
	return &cli.Command{
		Name:      "restore",
		Usage:     "Restore triples from a TSV file (key<TAB>value1<TAB>value2) into the current collection (additive); reads stdin if no file is given",
		ArgsUsage: "[file]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "drop",
				Usage: "Drop the existing collection first, overwriting it entirely with the restored data (default: merge)",
			},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			var src io.Reader = os.Stdin
			if path := ctx.Args().Get(0); path != "" {
				f, err := os.Open(path)
				if err != nil {
					return errors.New("Error opening file: " + err.Error())
				}
				defer f.Close()
				src = f
			}
			var triples [][3]string
			r := csv.NewReader(bufio.NewReader(src))
			r.Comma = '\t'
			r.FieldsPerRecord = 3
			line := 0
			for {
				line++
				rec, err := r.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fmt.Errorf("Malformed line %d: %v", line, err)
				}
				triples = append(triples, [3]string{rec[0], rec[1], rec[2]})
			}
			// The whole file is parsed first, so a malformed input aborts before
			// anything is dropped. The drop+restore that follows is not atomic,
			// though: if the restore fails mid-batch the collection is left empty.
			// Acceptable for a backup-recovery tool; re-run restore to retry.
			if ctx.Bool("drop") {
				if err := tie.DropCollection(); err != nil {
					return errors.New("Drop Error: " + err.Error())
				}
			}
			if err := tie.Restore(triples); err != nil {
				return errors.New("Restore Error: " + err.Error())
			}
			if err := tie.Sync(); err != nil {
				return errors.New("Sync Error: " + err.Error())
			}
			fmt.Fprintf(os.Stderr, "Restored %d triples\n", len(triples))
			return nil
		},
	}
}

func cmdVerify() *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "Check the virtual file tree for lost/incomplete nodes (fsck); exits non-zero if any problem is found",
		Description: "Verify scans the whole collection and reports structural and metadata problems:\n" +
			"orphaned directories/files (no parent edge — unreachable from the root), dangling\n" +
			"parent references, parent cycles, duplicate path claims, files missing core\n" +
			"metadata, and (with --check-blobs) files whose content is absent from the\n" +
			"filehost. It is read-only. --repair re-homes orphans under tie:/restored/<date>/\n" +
			"and is the only mutation; every other problem is reported, never auto-fixed.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "collection", Usage: "Collection to verify (default: config Collection)"},
			&cli.BoolFlag{Name: "repair", Usage: "Re-home orphaned files/dirs under a restored/ directory (default: report only)"},
			&cli.BoolFlag{Name: "check-blobs", Usage: "Also stat every file's content hash on the filehost (slow on a large store)"},
			&cli.StringFlag{Name: "dest", Usage: "Directory to restore orphans into (default: tie:/restored/<today>)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			collection := ctx.String("collection")
			rep, err := tie.Verify(collection, ctx.Bool("check-blobs"))
			if err != nil && rep == nil {
				return errors.New("Verify Error: " + err.Error())
			}
			printVerifyReport(rep)
			if err != nil {
				// Partial report (blob check failed after structural checks).
				return errors.New("Verify Error: " + err.Error())
			}
			if ctx.Bool("repair") {
				n, rerr := tie.RepairOrphans(collection, rep, ctx.String("dest"))
				if rerr != nil {
					return errors.New("Repair Error: " + rerr.Error())
				}
				if n > 0 {
					fmt.Fprintf(os.Stderr, "Restored %d orphaned node(s) under %s\n", n, restoreDestDisplay(ctx.String("dest")))
					// Re-verify so the reported problem count and exit code
					// reflect the post-repair state, not the pre-repair scan.
					rep, err = tie.Verify(collection, ctx.Bool("check-blobs"))
					if err != nil {
						return errors.New("Verify Error: " + err.Error())
					}
				}
			}
			if rep.Problems() > 0 {
				return cli.Exit(fmt.Sprintf("verify: %d problem(s) found", rep.Problems()), 1)
			}
			fmt.Fprintf(os.Stderr, "OK: %d directories, %d files — no problems\n", rep.DirCount, rep.FileCount)
			return nil
		},
	}
}

// restoreDestDisplay renders the effective restore directory for the summary
// line, mirroring RepairOrphans' default when --dest is empty.
func restoreDestDisplay(dest string) string {
	if dest != "" {
		return dest
	}
	return "tie:/restored/" + time.Now().Format("2006-01-02")
}

// printVerifyReport writes each non-empty problem class to stderr. The summary
// counts and OK line are printed separately by the caller; a fully-clean report
// prints nothing here.
func printVerifyReport(rep *client.VerifyReport) {
	w := os.Stderr
	section := func(title string, n int) {
		if n > 0 {
			fmt.Fprintf(w, "%s (%d):\n", title, n)
		}
	}
	section("Orphaned directories (no parent — unreachable)", len(rep.OrphanDirs))
	for _, u := range rep.OrphanDirs {
		fmt.Fprintf(w, "  %s\n", u)
	}
	section("Orphaned files (no parent — unreachable)", len(rep.OrphanFiles))
	for _, h := range rep.OrphanFiles {
		fmt.Fprintf(w, "  %s\n", h)
	}
	section("Dangling parent references (parent no longer exists)", len(rep.DanglingParentRefs))
	for _, e := range rep.DanglingParentRefs {
		fmt.Fprintf(w, "  %s -> %s\n", e.Child, e.Parent)
	}
	section("Parent cycles (a directory is its own ancestor)", len(rep.Cycles))
	for _, c := range rep.Cycles {
		parts := make([]string, len(c))
		for i, u := range c {
			parts[i] = string(u)
		}
		fmt.Fprintf(w, "  %s\n", strings.Join(parts, " -> "))
	}
	if len(rep.DuplicatePaths) > 0 {
		fmt.Fprintf(w, "Duplicate path claims (%d paths):\n", len(rep.DuplicatePaths))
		paths := make([]string, 0, len(rep.DuplicatePaths))
		for p := range rep.DuplicatePaths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			uids := rep.DuplicatePaths[p]
			parts := make([]string, len(uids))
			for i, u := range uids {
				parts[i] = string(u)
			}
			fmt.Fprintf(w, "  %s : %s\n", p, strings.Join(parts, ", "))
		}
	}
	section("Missing metadata (incomplete import)", len(rep.MissingMetadata))
	for _, g := range rep.MissingMetadata {
		kind := "file"
		if g.IsDir {
			kind = "dir "
		}
		fmt.Fprintf(w, "  %s %s : missing %s\n", kind, g.Subject, strings.Join(g.Missing, ", "))
	}
	section("Missing blobs (content absent from filehost)", len(rep.MissingBlobs))
	for _, h := range rep.MissingBlobs {
		fmt.Fprintf(w, "  %s\n", h)
	}
}

func cmdMount() *cli.Command {
	return &cli.Command{
		Name: "mount", Usage: "Mount a content-addressed directory (mount [dir-hash] [mountpoint]) " +
			"or the live tag-derived filesystem (mount --db [mountpoint])",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "Filehost name from config (default: first DefaultFileHosts)"},
			&cli.IntFlag{Name: "cache", Usage: "On-disk file cache size in GB", Value: 1},
			&cli.BoolFlag{Name: "db", Usage: "Mount the live tag-derived filesystem instead of a dir-hash"},
			&cli.BoolFlag{Name: "verify", Usage: "Verify downloaded bytes against their content hash (default off; use for an untrusted filehost)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}

			filehost, err := tie.ResolveHost(ctx.String("host"))
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
				state := fuselib.NewTieDBFuse(tie, filehost, ctx.Int("cache"), ctx.Bool("verify"))
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
				fmt.Println("\nUnmounting", mountpoint)
				if err := server.Unmount(); err != nil {
					fmt.Println("Unmount error:", err.Error())
				}
			}()

			fmt.Println("Mounted", what, "at", mountpoint, "(Ctrl-C to unmount)")
			server.Wait()

			return nil
		},
	}
}

func cmdDel() *cli.Command {
	return &cli.Command{
		Name:    "del",
		Aliases: []string{"d"},
		Usage:   "Delete a triple: del [key] [value1] [value2]",
		Action: func(_ context.Context, cCtx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()
			deleteBatch := func(filter string) error {
				rows, _, err := tie.Query(client.QuerySpec{
					Terms:  []string{args[0]},
					Filter: filter,
					Limit:  -1,
				})
				if err != nil {
					if errors.Is(err, client.ErrNotFound) {
						return nil
					}
					fmt.Println("Error:", err.Error())
					return nil
				}
				b := tie.NewBatch()
				for _, r := range rows {
					for relation, values := range r.Attributes {
						for _, v := range values {
							b.Delete(r.Key, relation, v)
						}
					}
				}
				if _, err := tie.Batch(b); err != nil {
					return errors.New("Delete Error: " + err.Error())
				}
				return nil
			}
			switch true {
			case args[1] == "*" && args[2] == "*":
				return deleteBatch("")

			case args[1] != "*" && args[2] == "*":
				return deleteBatch(args[1])

			case args[1] == "*" && args[2] != "*":
				fmt.Println("Function to delete specific value2 from all value1's is not implemented, because it is most likely a typo.")
				fmt.Println("Did you mean: tie delete", args[0], args[2], "* (delete all value2's from specific key + value1 pair?)")

			default:
				if _, err := tie.Delete(args[0], args[1], args[2]); err != nil {
					return errors.New("Delete Error: " + err.Error())
				}

			}

			return nil
		},
	}
}

func cmdConf() *cli.Command {
	return &cli.Command{
		Name:    "conf",
		Aliases: []string{"c"},
		Usage:   "conf",
		Commands: []*cli.Command{
			{
				Name:  "create",
				Usage: "Create new default config file: conf create [name]",
				Action: func(_ context.Context, cCtx *cli.Command) error {
					var name string
					if cCtx.Args().Len() == 0 {
						name = "config"
					} else {
						name = cCtx.Args().First()
					}
					err := client.SaveConfig(name, client.DefaultConfig())
					if err != nil {
						return err
					}
					path, err := conf.PathUserConfigDir("tie", client.ConfigFileName(name))
					fmt.Println("Config created here:", path)

					return err
				},
			},
		},
	}
}

// cmdTag groups tag-management subcommands. Tags attach to a content hash, so
// del/rename act on the tag everywhere that content appears in the current
// collection, and everywhere in the tag-query views.
func cmdTag() *cli.Command {
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					tags, _, err := tie.ListTags(ctx.Int("offset"), ctx.Int("limit"))
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					if err := tie.RegisterTag(ctx.Args().First()); err != nil {
						return errors.New("Tag add Error: " + err.Error())
					}
					return nil
				},
			},
			{
				Name:  "del",
				Usage: "Delete a tag from every item and the registry: tag del [tag]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					n, err := tie.DeleteTag(ctx.Args().First())
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 2 {
						return errors.New("Need 2 args: tag, newname")
					}
					n, err := tie.RenameTag(ctx.Args().Get(0), ctx.Args().Get(1))
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: tag")
					}
					files, _, err := tie.FilesWithTags("", []string{ctx.Args().First()}, nil, 0, -1)
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					files, _, err := tie.UntaggedFiles(ctx.String("type"), ctx.Int("offset"), ctx.Int("limit"))
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: hash")
					}
					tags, err := client.GetTags(tie, ctx.Args().First())
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need at least 1 arg: hash (pass no tags to clear)")
					}
					hash := ctx.Args().First()
					tags := ctx.Args().Tail()
					if err := client.SetTags(tie, hash, tags); err != nil {
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
func cmdVersions() *cli.Command {
	return &cli.Command{
		Name:  "versions",
		Usage: "Inspect and restore file version history: versions list, restore",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the superseded versions of a file (newest first): versions list [path]",
				Action: func(_ context.Context, ctx *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need 1 arg: path")
					}
					versions, err := tie.ListVersions("", ctx.Args().First())
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if ctx.Args().Len() < 1 {
						return errors.New("Need at least 1 arg: path (optional 2nd arg: version hash)")
					}
					hash, err := tie.RestoreVersion("", ctx.Args().Get(0), ctx.Args().Get(1))
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
func importFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "collection", Usage: "Collection to tag into (default: config Collection)"},
		&cli.StringSliceFlag{Name: "tags", Aliases: []string{"t"}},
		&cli.StringSliceFlag{Name: "host"},
		&cli.StringFlag{Name: "dest", Usage: "Virtual path to root the imported directory tree at (overrides config ImportDest and the source path)"},
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
func runImport(ctx *cli.Command, label string) error {
	if tie == nil {
		return errors.New("Error: Config not loaded")
	}
	dirType := label
	var forcedArchive client.TieType
	if t := client.StringToTieType(label); client.IsArchiveType(t) {
		// Archive subcommand: don't mislabel the directory root; force archive
		// files instead.
		forcedArchive = t
		dirType = client.TieDirectory.String()
	}
	for _, file := range ctx.Args().Slice() {
		fi, err := os.Stat(file)
		if err != nil {
			fmt.Println(err)
			continue
		}
		hosts := ctx.StringSlice("host")
		if len(hosts) == 0 {
			hosts = tie.Config.DefaultFileHosts
		}
		fmt.Println("Uploading to", hosts)
		for _, h := range hosts {
			if fi.IsDir() {
				if err := tie.ImportDir(file, tie.Config.FileHosts[h], ctx.String("collection"), dirType, ctx.StringSlice("tags"), ctx.String("dest"), forcedArchive); err != nil {
					fmt.Println(err)
				}
			} else {
				if err := tie.ImportFile(file, tie.Config.FileHosts[h], ctx.String("collection"), ctx.StringSlice("tags"), "", forcedArchive); err != nil {
					fmt.Println(err)
				}
			}
		}
	}
	return nil
}

// dirTypeImport builds an `import <name>` subcommand that labels imported
// directory roots with dirType (e.g. `import audio-dir album/`).
func dirTypeImport(name string) *cli.Command {
	usage := "import files, labeling directory roots as " + name
	if client.IsArchiveType(client.StringToTieType(name)) {
		usage = "import files, forcing archive files (zips) to " + name
	}
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Flags: importFlags(),
		Action: func(_ context.Context, ctx *cli.Command) error {
			return runImport(ctx, name)
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

func cmdImport(config client.Config) *cli.Command {
	subcommands := make([]*cli.Command, 0, len(builtinDirTypes)+len(builtinArchiveTypes)+len(config.ImportDest))
	for _, name := range importDirTypes(config) {
		subcommands = append(subcommands, dirTypeImport(name))
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
			return runImport(ctx, client.TieDirectory.String())
		},
		Commands: subcommands,
	}
}

// cmdStat prints a human-readable summary of a single item, addressed by either
// a VFS path or a 64-hex key (content hash or DirUID). The aggregation lives in
// the client (StatInfo/Stat/StatPath) so the GUI (tie-fm) shares it; this command
// only formats.
func cmdStat() *cli.Command {
	return &cli.Command{
		Name:  "stat",
		Usage: "Show info about a file or directory: stat [path-or-hash]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "check", Usage: "HEAD the filehost to confirm the blob exists and report its on-disk size"},
			&cli.BoolFlag{Name: "json", Usage: "Emit the raw StatInfo as JSON"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if ctx.Args().Len() < 1 {
				return errors.New("Need 1 arg: path or hash")
			}
			arg := ctx.Args().First()
			opts := client.StatOptions{Recursive: true, Versions: true, CheckBlob: ctx.Bool("check")}

			var (
				info client.StatInfo
				err  error
			)
			if metadata.IsHexHash(arg) {
				info, err = tie.Stat(arg, opts)
			} else {
				info, err = tie.StatPath(arg, opts)
			}
			if err != nil {
				if errors.Is(err, client.ErrNotFound) {
					fmt.Fprintln(os.Stderr, "no metadata in store for "+arg)
					return nil
				}
				return errors.New("Stat Error: " + err.Error())
			}

			if ctx.Bool("json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			printStat(info)
			return nil
		},
	}
}

// printStat renders a StatInfo as aligned LABEL/VALUE rows on a TTY, or plain
// tab-separated lines when stdout is piped (mirrors printRows).
func printStat(info client.StatInfo) {
	type kv struct{ k, v string }
	var rows []kv
	add := func(k, v string) {
		if v != "" {
			rows = append(rows, kv{k, v})
		}
	}

	typ := string(info.Kind)
	if info.TieType != client.TieDirectory && info.TieType.String() != "" {
		typ = string(info.Kind) + " (" + info.TieType.String() + ")"
	}
	add("Key", info.Key)
	add("Type", typ)
	add("Filename", info.Filename)
	add("Name", info.Name)
	add("Media type", info.MediaType)
	if info.Size > 0 || info.Kind == client.StatFile || info.Kind == client.StatArchive {
		add("Size", fmt.Sprintf("%s (%d bytes)", humanizeBytes(info.Size), info.Size))
	}
	if len(info.Tags) > 0 {
		add("Tags", strings.Join(info.Tags, ", "))
	}
	if !info.TagDate.IsZero() {
		add("Tagged", info.TagDate.Format(time.RFC3339))
	}
	for _, k := range []string{"title", "artist", "album", "year", "track"} {
		if v, ok := info.Meta[k]; ok {
			add(strings.ToUpper(k[:1])+k[1:], v)
		}
	}
	if len(info.Paths) > 0 {
		add("Path", strings.Join(info.Paths, ", "))
	}
	if info.Kind == client.StatDirectory {
		add("Children", fmt.Sprintf("%d dirs, %d files, %d archives", info.SubDirCount, info.FileCount, info.ArchiveCount))
	}
	if info.TotalSize > 0 {
		add("Total size", fmt.Sprintf("%s (%d bytes)", humanizeBytes(info.TotalSize), info.TotalSize))
	}
	if info.BlobChecked {
		if info.BlobExists {
			add("Blob", fmt.Sprintf("present (%s on disk)", humanizeBytes(info.BlobSize)))
		} else {
			add("Blob", "MISSING from filehost")
		}
	}
	if len(info.Versions) > 0 {
		add("Versions", strconv.Itoa(len(info.Versions)))
	}

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		for _, r := range rows {
			fmt.Println(r.k + "\t" + r.v)
		}
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, r.k+"\t"+r.v)
	}
	tw.Flush()
}

// humanizeBytes formats a byte count with a binary (KiB/MiB/…) unit, e.g.
// "1.4 MiB". Values under 1 KiB are shown as plain bytes.
func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
