package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/schollz/progressbar/v3"

	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"

	"git.sr.ht/~uid/conf"

	"git.sr.ht/~uid/tie/api"
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
			including := make([]string, 0)
			excluding := make([]string, 0)
			for i := 1; i < len(ctx.Args().Slice()); i++ {
				if strings.HasPrefix(args[i], "-") {
					excluding = append(excluding, strings.TrimPrefix(args[i], "-"))
				} else {
					including = append(including, args[i])
				}
			}
			o := api.GetOptions{
				Reverse: reverse,
				Include: including,
				Exclude: excluding,
				Filter:  ctx.String("filter"),
				Sort: tiedb.SortOptions{
					Offset: ctx.Int("offset"),
					Limit:  ctx.Int("limit"),
					SortBy: ctx.String("sortby"),
				},
			}
			reply, err := tie.Get(args[0], o)
			if err != nil {
				return errors.New("Get Error: " + err.Error())
			}
			for _, t := range reply.SortedResult {
				fmt.Println(t.Key + "\t" + t.Value1 + "\t" + t.Value2)
			}

			return nil
		},
	}
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
			var triples []tiedb.StringTriple
			if path := ctx.String("file"); path != "" {
				var err error
				triples, err = dumpLocalFile(path)
				if err != nil {
					return errors.New("Dump Error: " + err.Error())
				}
			} else {
				if tie == nil {
					return errors.New("Error: Config not loaded")
				}
				reply, err := tie.Dump()
				if err != nil {
					return errors.New("Dump Error: " + err.Error())
				}
				triples = reply.Triples
			}
			w := bufio.NewWriter(os.Stdout)
			defer w.Flush()
			for _, t := range triples {
				fmt.Fprintln(w, t.Key+"\t"+t.Value1+"\t"+t.Value2)
			}
			return nil
		},
	}
}

// dumpLocalFile reads every forward triple straight from an on-disk .tie file,
// bypassing the server. It reflects flushed on-disk state only, so it is meant
// for offline export when no daemon holds the file open.
func dumpLocalFile(path string) ([]tiedb.StringTriple, error) {
	if !strings.HasSuffix(path, ".tie") {
		return nil, fmt.Errorf("not a .tie file: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	name := strings.TrimSuffix(filepath.Base(path), ".tie")
	db := tiedb.NewDB(true)
	db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	col := db.GetCollection(tiedb.CollectionKey{Database: dir, Collection: name})
	var triples []tiedb.StringTriple
	col.ForEachTriple(func(t tiedb.StringTriple) {
		triples = append(triples, t)
	})
	return triples, nil
}

func cmdRestore() *cli.Command {
	return &cli.Command{
		Name:  "restore",
		Usage: "Restore triples from TSV (key<TAB>value1<TAB>value2) on stdin into the current collection (additive)",
		Action: func(_ context.Context, ctx *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			var triples [][3]string
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
			line := 0
			for scanner.Scan() {
				line++
				text := scanner.Text()
				if text == "" {
					continue
				}
				parts := strings.SplitN(text, "\t", 3)
				if len(parts) != 3 {
					return fmt.Errorf("Malformed line %d (need 3 tab-separated fields): %q", line, text)
				}
				triples = append(triples, [3]string{parts[0], parts[1], parts[2]})
			}
			if err := scanner.Err(); err != nil {
				return errors.New("Read Error: " + err.Error())
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
			&cli.IntFlag{Name: "cache", Usage: "In-memory file cache size in GB", Value: 1},
			&cli.BoolFlag{Name: "db", Usage: "Mount the live tag-derived filesystem instead of a dir-hash"},
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
				state := fuselib.NewTieDBFuse(tie, filehost.URL, filehost.Insecure, ctx.Int("cache"))
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
				state := fuselib.NewTieFuse(filehost.URL, filehost.Insecure, ctx.Int("cache"))
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
				o := api.GetOptions{
					Filter: filter,
					Sort:   tiedb.SortOptions{Limit: -1},
				}
				reply, err := tie.Get(args[0], o)
				if err != nil {
					fmt.Println("Error:", err.Error())
					return nil
				}
				b := tie.NewBatch()
				reply.Result.ForEachValue2(func(key, val1, val2 string) {
					b.Delete(key, val1, val2)
				})
				batchReply, err := tie.Batch(b)
				if err != nil {
					return errors.New("Delete Error: " + err.Error())
				}
				for _, d := range batchReply.DeleteReplys {
					if !d.Success {
						return errors.New("First Delete Error: " + d.Message)
					}
				}
				return nil
			}
			switch true {
			case args[1] == "*" && args[2] == "*":
				return deleteBatch(args[1])

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
			// {
			// 	Name:  "remove",
			// 	Usage: "remove an existing template",
			// 	Action: func(cCtx *cli.Context) error {
			// 		fmt.Println("removed task template: ", cCtx.Args().First())
			// 		return nil
			// 	},
			// },
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

// func cmdList() []*cobra.Command {
// 	cmds := []*cobra.Command{}

// var cmdSetState = &cobra.Command{
// 	Use:   "set [webservice] [namespace] [collection]",
// 	Short: "Set current namespace + collection",
// 	Long:  `Set current namespace + collection.`,
// 	Args:  cobra.MinimumNArgs(3),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.CurrentState.Webservice = args[0]
// 		tie.CurrentState.Namespace = args[1]
// 		tie.CurrentState.Collection = args[2]
// 		tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
// 		tie.PrintState()
// 	},
// }
// cmds = append(cmds, cmdSetState)

// var cmdSetCollection = &cobra.Command{
// 	Use:   "col [collection]",
// 	Short: "Set current collection",
// 	Long:  `Set current collection.`,
// 	Args:  cobra.MinimumNArgs(1),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.CurrentState.Collection = args[0]
// 		tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
// 		tie.PrintState()
// 	},
// }
// cmds = append(cmds, cmdSetCollection)

// minArgs := 3
// if len(stdin) != 0 {
// 	minArgs = 2
// }
// var cmdAdd = &cobra.Command{
// 	Use:   "add [key] [value1] [value2]",
// 	Short: "Associate two entries.",
// 	Long:  `Associate two entries.`,
// 	Args:  cobra.MinimumNArgs(minArgs),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		if minArgs == 2 {
// 			// for _, x := range stdin {
// 			// a := request.NewAddRequest(x.hash, args[0], args[1])
// 			// AddHandler(tie.Run(a))
// 			// }
// 		} else {
// 			tie.Add(args[0], args[1], args[2], func(reply *api.AddReply) {
// 				if !reply.Success {
// 					fmt.Println("Error while adding!", "Message:", reply.Message)
// 				}
// 			})
// 		}
// 	},
// }
// cmds = append(cmds, cmdAdd)

// var getFilter string
// var cmdGet = &cobra.Command{
// 	Use:   "get [key]",
// 	Short: "Return triple with [key]",
// 	Long:  "Return triple with [key]",
// 	Args:  cobra.MinimumNArgs(1),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.Get(args[0], func(reply *api.GetReply) {
// 			if reply.Success {
// 				reply.Result.ForEachValue2(func(key, value1, value2 string) {
// 					// fmt.Printf("%s%20s%20s\n", key, value1, value2)
// 					fmt.Println(key + "\t" + value1 + "\t" + value2)
// 				})
// 			} else {
// 				fmt.Println("Error getting:", args[0])
// 				fmt.Println("Message:", reply.Message)

// 			}
// 		})
// 	},
// }
// cmdGet.Flags().StringVarP(&getFilter, "filter", "f", "", "Value1 filter")

// cmds = append(cmds, cmdGet)

// // ---- Get 2

// var cmdGet2 = &cobra.Command{
// 	Use:   "get2 [key] [key2] ... [keyN]",
// 	Short: "Return tie with [key] or Join multiple keys on their 'associated' value",
// 	Long:  "Return tie with [key] or Join multiple keys on their 'associated' value",
// 	Args:  cobra.MinimumNArgs(1),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		a := request.NewGet2Request(args)
// 		if len(args) > 1 {
// 			a.Filter = tiedb.ASSOCIATED
// 		}
// 		if getFilter != "" {
// 			a.Filter = getFilter
// 		}
// 		Get2Handler(tie.Run(a))
// 	},
// }
// cmdGet2.Flags().StringVarP(&getFilter, "filter", "f", "", "Relation filter")

// cmds = append(cmds, cmdGet2)

// // ---- End Get 2

// var cmdDel = &cobra.Command{
// 	Use:   "del [key] [value1] [value2]",
// 	Short: "Delete tie",
// 	Long:  `Delete tie`,
// 	Args:  cobra.MinimumNArgs(3),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.Delete(args[0], args[1], args[2], func(reply *api.DeleteReply) {
// 			if !reply.Success {
// 				fmt.Println("Error while deleting:", reply.Message)
// 			}
// 		})
// 	},
// }
// cmds = append(cmds, cmdDel)

// // var cmdFilter = &cobra.Command{
// // 	Use:   "filter [value] [value2]...",
// // 	Short: "Filter associations",
// // 	Long:  `Filter associations`,
// // 	Args:  cobra.MinimumNArgs(2),
// // 	Run: func(cmd *cobra.Command, args []string) {
// // 	},
// // }
// // // cmdFilter.fla
// // // localCmd.Flags().StringVarP(&Source, "source", "s", "", "Source directory to read from")
// // cmds = append(cmds, cmdFilter)

// minArgsTag := 1
// if len(stdin) != 0 {
// 	minArgsTag = 0
// }
// var cmdTag = &cobra.Command{
// 	Use:   "tag [file|dir] [flags...]",
// 	Short: "tag current dir",
// 	Long:  `tag current dir`,
// 	Args:  cobra.MinimumNArgs(minArgsTag),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		options := tie.TagOptions{}
// 		if minArgsTag == 0 {
// 			for _, x := range stdin {
// 				tie.Tag(x.hash, args, options, OldAddHandler)
// 			}
// 		} else {
// 			tie.Tag(args[0], args[1:], options, OldAddHandler)
// 		}
// 	},
// }
// cmds = append(cmds, cmdTag)

// var cmdBatch = &cobra.Command{
// 	Use:   "batch [cmd:key] [cmd:key2] ... [cmd:keyN]",
// 	Short: "batch requests (probably temp command)",
// 	Long:  "batch requests (probably temp command)",
// 	Args:  cobra.MinimumNArgs(1),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		b := request.NewBatchRequest()
// 		for _, x := range args {
// 			part := strings.Split(x, ":")

// 			if len(part) < 2 {
// 				fmt.Println("Need input args in format 'cmd:key', like get:music")
// 				return
// 			}

// 			switch strings.ToLower(part[0]) {
// 			case "get":
// 				a := request.NewGetRequest([]string{strings.Join(part[1:], ":")})
// 				// if len(args) > 1 {
// 				// 	a.Filter = tiedb.ASSOCIATED
// 				// }
// 				if getFilter != "" {
// 					a.Filter = getFilter
// 				}
// 				b.Get = append(b.Get, a)
// 			}
// 		}

// 		BatchHandler(tie.Run(b))
// 	},
// }
// cmdBatch.Flags().StringVarP(&getFilter, "filter", "f", "", "Relation filter")
// cmds = append(cmds, cmdBatch)

// return cmds
// }

// func OldAddHandler(resp json.RawMessage) {
// s := request.ReplyStatus{}
// if err := json.Unmarshal(resp, &s); err == nil {
// 	if tie.CurrentState.Verbose || !s.Success {
// 		fmt.Println(resp)
// 	}
// } else {
// 	fmt.Println("Error unmarshalling response:", err, resp)
// }

// }

// func AddHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Add' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// if tie.CurrentState.Verbose {
// 	fmt.Println(string(reply.ReplyRawResponse))
// }
// var result = *reply.DataStatus()
// if !result.Success {
// 	fmt.Println("Success:", result.Success, "Message:", result.Message)
// }
// }

// func GetHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Get' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataGet()
// var input tie.TieOutput
// var columns []string
// for _, x := range result {
// 	for i, _ := range x.Value2 {
// 		key := x.Item
// 		value1 := x.Value1[i]
// 		value2 := x.Value2[i]
// 		if outputAsTable {
// 			tie.LoadTieOutput(key, value1, value2, &input, &columns)
// 		} else {
// 			fmt.Println(key + "\t" + value1 + "\t" + value2)
// 		}
// 	}
// }
// if outputAsTable {
// 	table := tie.TieOutputToTable(input, columns)
// 	table.Print(columns)
// }
// }

// func Get2Handler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Get' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataGet2()
// // var input tie.TieOutput
// // var columns []string

// result.ForEach(func(key, value1, value2 string) {
// 	fmt.Println(key + "\t" + value1 + "\t" + value2)
// })

// result["file"].ForEach(func(value1, value2 string) {
// 	fmt.Println(value1 + "\t" + value2)
// })

// // key := x.Item
// // value1 := x.Value1[i]
// // value2 := x.Value2[i]
// // if outputAsTable {
// // 	tie.LoadTieOutput(key, value1, value2, &input, &columns)
// // } else {
// // }
// // }
// // }
// // }
// // if outputAsTable {
// // 	table := tie.TieOutputToTable(input, columns)
// // 	table.Print(columns)
// // }
// }

// func DeleteHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Delete' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataStatus()
// fmt.Println("Success:", result.Success, "Message:", result.Message)

// }

// func BatchHandler(reply *request.Reply, err error) {
//
//	if err != nil {
//		fmt.Println("Error handling 'Batch' reponse:", err.Error())
//		fmt.Println("Received:", string(reply.ReplyRawResponse))
//	}
//
// var result = *reply.DataBatch()
// var input tie.TieOutput
// var columns []string
//
//	for _, a := range result.Get {
//		for _, x := range a {
//			for i, _ := range x.Value2 {
//				key := x.Item
//				value1 := x.Value1[i]
//				value2 := x.Value2[i]
//				if outputAsTable {
//					tie.LoadTieOutput(key, value1, value2, &input, &columns)
//				} else {
//					fmt.Println(key + "\t" + value1 + "\t" + value2)
//				}
//			}
//		}
//	}
//
//	if outputAsTable {
//		table := tie.TieOutputToTable(input, columns)
//		table.Print(columns)
//	}
// }
