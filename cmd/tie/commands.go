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

func cmdMount() *cli.Command {
	return &cli.Command{
		Name: "mount",
		Usage: "Mount a content-addressed directory (mount [dir-hash] [mountpoint]) " +
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
				state := fuselib.NewTieDBFuse(tie, filehost.URL, filehost.Insecure, ctx.Int("cache"), ctx.Bool("verify"))
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
				state := fuselib.NewTieFuse(filehost.URL, filehost.Insecure, ctx.Int("cache"), ctx.Bool("verify"))
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
					path, err := conf.PathUserConfigDir("tie", name)
					fmt.Println("Config created here:", path)

					return err
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

// runImport uploads and tags every path argument. Each file's own tie-type is
// detected from its contents; dirType only labels the root of an imported
// directory tree (files inside are still detected individually).
func runImport(ctx *cli.Command, dirType string) error {
	if tie == nil {
		return errors.New("Error: Config not loaded")
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
				if err := tie.ImportDir(file, tie.Config.FileHosts[h], ctx.String("collection"), dirType, ctx.StringSlice("tags"), ctx.String("dest")); err != nil {
					fmt.Println(err)
				}
			} else {
				if err := tie.ImportFile(file, tie.Config.FileHosts[h], ctx.String("collection"), ctx.StringSlice("tags"), ""); err != nil {
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
	return &cli.Command{
		Name:  name,
		Usage: "import files, labeling directory roots as " + name,
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
	seen := make(map[string]bool, len(builtinDirTypes))
	types := make([]string, 0, len(builtinDirTypes)+len(config.ImportDest))
	for _, name := range builtinDirTypes {
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
	subcommands := make([]*cli.Command, 0, len(builtinDirTypes)+len(config.ImportDest))
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
