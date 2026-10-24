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
				component := "triplestore"
				if a.tie.IsLocal() {
					component = "triplestore (local file)"
				}
				probe(component, a.tie.Target(), v, err)

				// In an offline run with no config there is no filehost to probe;
				// the built-in default would only report a misleading failure.
				if a.configLoaded {
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
		Name:  "conf",
		Usage: "Create and inspect the configuration",
		Description: "The config is looked up in this order: the -C/--config value, $TIE_CONFIG,\n" +
			"./config.toml, then the user config dir (~/.config/tie) and the system\n" +
			"config dir (/etc/tie). The .toml extension may be omitted for a name.\n" +
			"No command writes a config except `conf create` (§6.6).",
		Commands: []*cli.Command{
			{
				Name:  "show",
				Usage: "Print the collection, filehosts and URLs this invocation actually resolves to",
				Description: "Config resolution has several layers — the flat keys, [Collections], -c,\n" +
					"-C, $TIE_CONFIG — and getting them wrong is the likeliest way to run a\n" +
					"command against the wrong store. This prints what *this* invocation means:\n" +
					"the file it loaded, the collection it resolved, the triplestore URL, the\n" +
					"filehosts an upload would pick. Passwords are never printed.",
				Action: func(_ context.Context, cCtx *cli.Command) error {
					if a.tie == nil {
						return exitError{code: exitUsage, msg: a.configMessage()}
					}
					o, err := a.outFor(cCtx, "field", "value")
					if err != nil {
						return err
					}
					for _, row := range confRows(a.config, cCtx.String("collection")) {
						o.row(row[0], row[1])
					}
					return o.flush()
				},
			},
			{
				Name:  "create",
				Usage: "Create new default config file: conf create [name]",
				Action: func(_ context.Context, cCtx *cli.Command) error {
					// With no argument, create the config this invocation would
					// have loaded — the -C value or $TIE_CONFIG — so the fix the
					// refusal points at actually fixes the next run (§6.6).
					name := cCtx.String("config")
					if cCtx.Args().Len() > 0 {
						name = cCtx.Args().First()
					}
					path, err := client.SaveConfigTo(name, client.DefaultConfig())
					if err != nil {
						return operationalError("creating config: %v", err)
					}
					fmt.Fprintln(os.Stderr, "Config created here:", path)
					fmt.Fprintln(os.Stderr, "Set Username and Password there if the triplestore requires authentication.")
					return nil
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
		Usage: "Show info about files or directories: stat PATH-or-HASH... (- reads them from stdin)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "check", Usage: "HEAD the filehost to confirm the blob exists and report its on-disk size"},
			&cli.BoolFlag{Name: "json", Usage: "Emit the raw StatInfo as JSON (same as --format json)"},
			stdinFlag(),
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			keys, err := argKeys(ctx)
			if err != nil {
				return err
			}
			if len(keys) < 1 {
				return exitError{code: exitUsage, msg: "stat takes a PATH or HASH (or - to read them from stdin)"}
			}
			opts := client.StatOptions{Recursive: true, Versions: true, CheckBlob: ctx.Bool("check")}

			format, err := outputFormat(ctx)
			if err != nil {
				return err
			}
			asJSON := ctx.Bool("json") || format == "json"

			var infos []client.StatInfo
			for _, arg := range keys {
				var info client.StatInfo
				if metadata.IsHexHash(arg) {
					info, err = a.tie.Stat(arg, opts)
				} else {
					info, err = a.tie.StatPath(arg, opts)
				}
				if err != nil {
					if errors.Is(err, client.ErrNotFound) {
						fmt.Fprintln(os.Stderr, "no metadata in store for "+arg)
						continue
					}
					return operationalError("stat: %v", err)
				}
				infos = append(infos, info)
			}
			if len(infos) == 0 {
				return nil
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if len(infos) == 1 {
					return enc.Encode(infos[0])
				}
				return enc.Encode(infos)
			}
			o, err := a.outFor(ctx, "field", "value")
			if err != nil {
				return err
			}
			for _, info := range infos {
				printStat(o, info)
			}
			return o.flush()
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
	return nil
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

// confRows resolves the config the way a command will use it and reports what
// it settled on (§9: config resolution — two URL keys, flat keys vs
// [Collections], -c vs --collection — is where misconfiguration hides).
// Passwords are deliberately not among them.
func confRows(cfg client.Config, collectionFlag string) [][]string {
	resolved := cfg.ResolveCollection(collectionFlag)
	store := resolved.TripleStoreURL
	if resolved.TripleStoreFile != "" {
		store = "file:" + resolved.TripleStoreFile
	}
	rows := [][]string{
		{"config", cfg.Path()},
		{"collection", resolved.Collection},
		{"namespace", resolved.Namespace},
		{"triplestore", store},
		{"username", resolved.Username},
		{"insecure", strconv.FormatBool(resolved.Insecure)},
		{"filehosts", strings.Join(resolved.FileHosts, ", ")},
		{"prev-versions", strconv.Itoa(cfg.PrevVersions)},
	}
	if resolved.TripleStoreFile != "" {
		rows = append(rows, []string{"db-file", resolved.TripleStoreFile})
	}

	names := make([]string, 0, len(cfg.FileHosts))
	for name := range cfg.FileHosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rows = append(rows, []string{"filehost:" + name, cfg.FileHosts[name].URL})
	}

	if len(cfg.Collections) > 0 {
		defined := make([]string, 0, len(cfg.Collections))
		for name := range cfg.Collections {
			defined = append(defined, name)
		}
		sort.Strings(defined)
		rows = append(rows, []string{"collections", strings.Join(defined, ", ")})
	}

	dests := make([]string, 0, len(cfg.ImportDest))
	for name := range cfg.ImportDest {
		dests = append(dests, name)
	}
	sort.Strings(dests)
	for _, name := range dests {
		rows = append(rows, []string{"import-dest:" + name, cfg.ImportDest[name]})
	}
	return rows
}
