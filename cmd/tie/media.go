package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/uidbz/tie/client"
)

// resolveSubject maps a CLI argument to a subject: a virtual path (leading
// "/" or "tie:") resolves to its DirUID, anything else is taken verbatim.
func resolveSubject(a *app, arg string) (string, error) {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, client.FileURIScheme) {
		uid, err := a.tie.DirUIDFromPath(arg)
		if err != nil {
			return "", err
		}
		if uid == "" {
			return "", fmt.Errorf("%s: no such directory", arg)
		}
		return string(uid), nil
	}
	return arg, nil
}

// parseSince accepts a Go duration ("72h"), a day count ("30d") or a date
// (2026-01-31).
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().AddDate(0, 0, -n), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: want a duration (72h), days (30d) or a date (2026-01-31)", s)
}

// indexResultRow prints one index pass as data: subject and the counts,
// tab-separated in a fixed order. The old `members=… added=…` form mixed
// labels into what should be a parseable row (§6.2).
func indexResultRow(o *out, subject string, r client.IndexResult) {
	o.row(subject,
		strconv.Itoa(r.Members), strconv.Itoa(r.AddedMembers), strconv.Itoa(r.DroppedStale),
		strconv.Itoa(r.Discs), strconv.Itoa(r.Ops))
}

// countRow prints a command's result count as a data row — label and value,
// the shape `tie stat` already prints — instead of a sentence (§6.2).
func countRow(o *out, label string, n int) {
	o.row(label, strconv.Itoa(n))
}

func cmdMedia(a *app) *cli.Command {
	return &cli.Command{
		Name:  "media",
		Usage: "Media collection model: index, backfill, rescan, restat",
		Commands: []*cli.Command{
			{
				Name:      "index",
				Usage:     "Bring one collection up to the collection contract (member-of, added-at, disc labels, album summary)",
				ArgsUsage: "<path|uid>...",
				Flags:     []cli.Flag{&cli.BoolFlag{Name: "dry-run", Usage: "Report what would change"}},
				Action: func(_ context.Context, c *cli.Command) error {
					if c.Args().Len() == 0 {
						return errors.New("need at least one collection path or uid")
					}
					o, err := a.outFor(c, "subject", "members", "added", "stale", "discs", "ops")
					if err != nil {
						return err
					}
					for _, arg := range c.Args().Slice() {
						uid, err := resolveSubject(a, arg)
						if err != nil {
							return err
						}
						r, err := a.tie.IndexCollection(client.DirUID(uid), client.IndexOptions{DryRun: c.Bool("dry-run")})
						if err != nil {
							return err
						}
						indexResultRow(o, uid, r)
					}
					return o.flush()
				},
			},
			{
				Name:  "backfill",
				Usage: "Index every collection in the store (data imported before the collection contract)",
				Description: "One row per changed collection on stdout, tab-separated: subject,\n" +
					"members, added-members, dropped-stale, discs, ops; then the summary\n" +
					"as label/value rows.",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Usage: "Report what would change"},
					&cli.StringSliceFlag{Name: "type", Usage: "Only collections with this label (repeatable; default: all)"},
					&cli.BoolFlag{Name: "rescan", Usage: "Also re-read audio tags from the filehost for tracks predating genre/disc/composer (downloads file heads)"},
					&cli.BoolFlag{Name: "playlists", Value: true, Usage: "Also migrate legacy playlists"},
					&cli.BoolFlag{Name: "names", Value: true, Usage: "Also recompute name triples from filename (fixes pre-0.6 TrimRight corruption)"},
				},
				Action: func(_ context.Context, c *cli.Command) error {
					dry := c.Bool("dry-run")
					o, err := a.outFor(c, "subject", "members", "added", "stale", "discs", "ops")
					if err != nil {
						return err
					}
					if c.Bool("rescan") {
						n, err := a.tie.RescanAudio(client.RescanOptions{DryRun: dry, Progress: func(done, total int, hash string, err error) {
							if err != nil {
								fmt.Fprintf(os.Stderr, "rescan %s: %v\n", hash, err)
							}
							if done%100 == 0 || done == total {
								fmt.Fprintf(os.Stderr, "rescanned %d/%d\n", done, total)
							}
						}})
						countRow(o, "rescanned-tracks", n)
						if err != nil {
							fmt.Fprintln(os.Stderr, "rescan: first error:", err)
						}
					}
					if c.Bool("playlists") && !dry {
						n, err := a.tie.MigrateLegacyPlaylists()
						if err != nil {
							return err
						}
						countRow(o, "migrated-playlists", n)
					}
					if c.Bool("names") {
						n, err := a.tie.RepairNames()
						if err != nil {
							return err
						}
						countRow(o, "names-repaired", n)
					}
					var types []string
					if t := c.StringSlice("type"); len(t) > 0 {
						types = t
					}
					res, err := a.tie.Backfill(client.BackfillOptions{DryRun: dry, Types: types, Progress: func(done, total int, r client.IndexResult, err error) {
						if err != nil {
							fmt.Fprintln(os.Stderr, err)
						} else if r.Ops > 0 {
							indexResultRow(o, string(r.Collection), r)
						}
					}})
					if err != nil {
						return err
					}
					countRow(o, "collections", res.Collections)
					countRow(o, "changed", res.Changed)
					countRow(o, "archives-stamped", res.Archives)
					countRow(o, "errors", len(res.Errors))
					return o.flush()
				},
			},
			{
				Name:  "restat",
				Usage: "Rebuild play-count / last-played statistics from the play events",
				Action: func(_ context.Context, c *cli.Command) error {
					o, err := a.outFor(c, "result", "count")
					if err != nil {
						return err
					}
					n, err := a.tie.RecomputeStats()
					if err != nil {
						return err
					}
					countRow(o, "subjects-updated", n)
					return o.flush()
				},
			},
			{
				Name:  "schema",
				Usage: "Declare the media value types (year, duration, added-at, …)",
				Action: func(_ context.Context, c *cli.Command) error {
					return a.tie.EnsureMediaSchema()
				},
			},
		},
	}
}

func cmdHistory(a *app) *cli.Command {
	return &cli.Command{
		Name:  "history",
		Usage: "List recorded plays, newest first",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "since", Usage: "Only plays after: 72h, 30d or 2026-01-31"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 50, Usage: "Max plays (0 = all)"},
			&cli.BoolFlag{Name: "skips", Usage: "Include skipped plays"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			since, err := parseSince(c.String("since"))
			if err != nil {
				return err
			}
			q := client.PlayQuery{Since: since, Limit: int(c.Int("limit"))}
			if !c.Bool("skips") {
				q.Outcome = client.OutcomeComplete
				if q.Limit > 0 {
					q.Limit *= 2 // headroom for filtered-out skips
				}
			}
			events, err := a.tie.RecentPlays(q)
			if err != nil {
				return err
			}
			if lim := int(c.Int("limit")); lim > 0 && len(events) > lim {
				events = events[:lim]
			}
			keys := make([]string, 0, len(events))
			for _, e := range events {
				keys = append(keys, e.Subject)
			}
			names := map[string]string{}
			if rows, err := a.tie.Expand(keys); err == nil {
				for _, r := range rows {
					n := client.RowFirst(r, "title")
					if artist := client.RowFirst(r, "artist"); artist != "" && n != "" {
						n = artist + " — " + n
					}
					if n == "" {
						n = client.RowFirst(r, "filename")
					}
					names[r.Key] = n
				}
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			for _, e := range events {
				fmt.Fprintf(w, "%s\t%s\t%.0fs\t%s\t%s\n", e.PlayedAt.Local().Format("2006-01-02 15:04"), e.Outcome, e.Listened, names[e.Subject], e.Subject)
			}
			return w.Flush()
		},
	}
}

func cmdPlaylist(a *app) *cli.Command {
	return &cli.Command{
		Name:  "playlist",
		Usage: "Manage playlists: list, show, create, delete, migrate",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List playlists",
				Action: func(_ context.Context, c *cli.Command) error {
					ps, err := a.tie.ListPlaylists()
					if err != nil {
						return err
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
					for _, p := range ps {
						legacy := ""
						if p.Legacy {
							legacy = "(legacy)"
						}
						fmt.Fprintf(w, "%s\t%s\t%d tracks\t%s\n", p.UID, p.Name, len(p.Tracks), legacy)
					}
					return w.Flush()
				},
			},
			{
				Name:      "show",
				Usage:     "Print a playlist's track hashes in order",
				ArgsUsage: "<uid>",
				Action: func(_ context.Context, c *cli.Command) error {
					p, err := a.tie.ReadPlaylist(c.Args().First())
					if err != nil {
						return err
					}
					for _, h := range p.Tracks {
						fmt.Println(h)
					}
					return nil
				},
			},
			{
				Name:      "create",
				Usage:     "Create a playlist from track hashes",
				ArgsUsage: "<name> <hash>...",
				Action: func(_ context.Context, c *cli.Command) error {
					if c.Args().Len() < 1 {
						return errors.New("need a name")
					}
					uid, err := a.tie.CreatePlaylist(c.Args().First(), c.Args().Tail(), nil)
					if err != nil {
						return err
					}
					fmt.Println(uid)
					return nil
				},
			},
			{
				Name:      "delete",
				Usage:     "Delete a playlist (tracks are untouched)",
				ArgsUsage: "<uid>",
				Action: func(_ context.Context, c *cli.Command) error {
					return a.tie.DeletePlaylist(c.Args().First())
				},
			},
			{
				Name:  "migrate",
				Usage: "Convert legacy (audio-dir) playlists to the playlist type",
				Action: func(_ context.Context, c *cli.Command) error {
					o, err := a.outFor(c, "result", "count")
					if err != nil {
						return err
					}
					n, err := a.tie.MigrateLegacyPlaylists()
					if err != nil {
						return err
					}
					countRow(o, "migrated", n)
					return o.flush()
				},
			},
		},
	}
}
