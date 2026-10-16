package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/conf"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/version"
	"github.com/urfave/cli/v3"
)

// cmdVersion prints the client build and, when a config is loaded, the build
// each configured server is running — the quick way to confirm a deploy landed
// on a remote triplestore/filehost. Servers that cannot be reached are
// reported inline rather than failing the command.
func cmdVersion(a *app) *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Show the client version and the versions of the configured triplestore and filehosts",
		Description: "One row per component, tab-separated: component, version, URL.\n" +
			"A configured server that cannot be reached prints `unavailable` and\n" +
			"makes the command exit non-zero, so `tie version` works as a health\n" +
			"check (§6.2). Why it failed goes to stderr.",
		Action: func(_ context.Context, ctx *cli.Command) error {
			type row struct{ component, ver, url string }
			rows := []row{{"client", describeBuild(version.Get()), "-"}}
			unreachable := 0
			probe := func(component, url string, v version.Info, err error) {
				if err != nil {
					unreachable++
					fmt.Fprintf(os.Stderr, "tie: %s unreachable at %s: %v\n", component, url, err)
					rows = append(rows, row{component, "unavailable", url})
					return
				}
				rows = append(rows, row{component, describeBuild(v), url})
			}

			if a.tie != nil {
				v, err := a.tie.ServerVersion()
				probe("triplestore", a.tie.Config.TripleStoreURL, v, err)

				names := make([]string, 0, len(a.tie.Config.FileHosts))
				for name := range a.tie.Config.FileHosts {
					names = append(names, name)
				}
				sort.Strings(names)
				seen := map[string]bool{}
				for _, name := range names {
					host := a.tie.Config.FileHosts[name]
					if seen[host.URL] {
						continue
					}
					seen[host.URL] = true
					v, err := client.FileHostVersion(host)
					probe("filehost:"+name, host.URL, v, err)
				}
			}

			for _, r := range rows {
				fmt.Println(r.component + "\t" + r.ver + "\t" + r.url)
			}
			if unreachable > 0 {
				return exitError{code: 1, msg: fmt.Sprintf("%d %s unreachable", unreachable, plural(unreachable, "server", "servers"))}
			}
			return nil
		},
	}
}

// describeBuild renders a build as "version (commit, go)" with the commit
// shown only when it adds information the version string lacks.
func describeBuild(v version.Info) string {
	s := v.Version
	var extra []string
	if v.Commit != "" && !strings.Contains(v.Version, v.Commit[:min(7, len(v.Commit))]) {
		extra = append(extra, v.Commit[:min(12, len(v.Commit))])
	}
	if v.Dirty && !strings.HasSuffix(v.Version, "-dirty") && !strings.HasSuffix(v.Version, "+dirty") {
		extra = append(extra, "dirty")
	}
	if v.GoVersion != "" {
		extra = append(extra, v.GoVersion)
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

func cmdConf(a *app) *cli.Command {
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
					fmt.Fprintln(os.Stderr, "Config created here:", path)

					return err
				},
			},
		},
	}
}

// cmdTag groups tag-management subcommands. Tags attach to a content hash, so
// del/rename act on the tag everywhere that content appears in the current
// collection, and everywhere in the tag-query views.

// cmdStat prints a human-readable summary of a single item, addressed by either
// a VFS path or a 64-hex key (content hash or DirUID). The aggregation lives in
// the client (StatInfo/Stat/StatPath) so the GUI (tie-fm) shares it; this command
// only formats.
func cmdStat(a *app) *cli.Command {
	return &cli.Command{
		Name:  "stat",
		Usage: "Show info about a file or directory: stat [path-or-hash]",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "check", Usage: "HEAD the filehost to confirm the blob exists and report its on-disk size"},
			&cli.BoolFlag{Name: "json", Usage: "Emit the raw StatInfo as JSON (same as --format json)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
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
				info, err = a.tie.Stat(arg, opts)
			} else {
				info, err = a.tie.StatPath(arg, opts)
			}
			if err != nil {
				if errors.Is(err, client.ErrNotFound) {
					fmt.Fprintln(os.Stderr, "no metadata in store for "+arg)
					return nil
				}
				return errors.New("Stat Error: " + err.Error())
			}

			format, err := outputFormat(ctx)
			if err != nil {
				return err
			}
			if ctx.Bool("json") || format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			o, err := a.outFor(ctx, "field", "value")
			if err != nil {
				return err
			}
			return printStat(o, info)
		},
	}
}

// printStat writes a StatInfo as field/value rows through the shared renderer,
// so it follows the global --format like every other listing (§6.4).
func printStat(o *out, info client.StatInfo) error {
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

	for _, r := range rows {
		o.row(r.k, r.v)
	}
	return o.flush()
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
