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
func resolveSubject(arg string) (string, error) {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, client.FileURIScheme) {
		uid, err := tie.DirUIDFromPath(arg)
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

func cmdMedia() *cli.Command {
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if c.Args().Len() == 0 {
						return errors.New("need at least one collection path or uid")
					}
					for _, a := range c.Args().Slice() {
						uid, err := resolveSubject(a)
						if err != nil {
							return err
						}
						r, err := tie.IndexCollection(client.DirUID(uid), client.IndexOptions{DryRun: c.Bool("dry-run")})
						if err != nil {
							return err
						}
						fmt.Printf("%s\tmembers=%d added=%d stale=%d discs=%d ops=%d\n", uid, r.Members, r.AddedMembers, r.DroppedStale, r.Discs, r.Ops)
					}
					return nil
				},
			},
			{
				Name:  "backfill",
				Usage: "Index every collection in the store (data imported before the collection contract)",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Usage: "Report what would change"},
					&cli.StringSliceFlag{Name: "type", Usage: "Only collections with this label (repeatable; default: all)"},
					&cli.BoolFlag{Name: "rescan", Usage: "Also re-read audio tags from the filehost for tracks predating genre/disc/composer (downloads file heads)"},
					&cli.BoolFlag{Name: "playlists", Value: true, Usage: "Also migrate legacy playlists"},
					&cli.BoolFlag{Name: "names", Value: true, Usage: "Also recompute name triples from filename (fixes pre-0.6 TrimRight corruption)"},
				},
				Action: func(_ context.Context, c *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					dry := c.Bool("dry-run")
					if c.Bool("rescan") {
						n, err := tie.RescanAudio(client.RescanOptions{DryRun: dry, Progress: func(done, total int, hash string, err error) {
							if err != nil {
								fmt.Fprintf(os.Stderr, "rescan %s: %v\n", hash, err)
							}
							if done%100 == 0 || done == total {
								fmt.Fprintf(os.Stderr, "rescanned %d/%d\n", done, total)
							}
						}})
						fmt.Printf("rescanned tracks: %d\n", n)
						if err != nil {
							fmt.Fprintln(os.Stderr, "rescan: first error:", err)
						}
					}
					if c.Bool("playlists") && !dry {
						n, err := tie.MigrateLegacyPlaylists()
						if err != nil {
							return err
						}
						fmt.Printf("migrated playlists: %d\n", n)
					}
					if c.Bool("names") {
						n, err := tie.RepairNames("")
						if err != nil {
							return err
						}
						fmt.Printf("names repaired: %d\n", n)
					}
					var types []string
					if t := c.StringSlice("type"); len(t) > 0 {
						types = t
					}
					res, err := tie.Backfill(client.BackfillOptions{DryRun: dry, Types: types, Progress: func(done, total int, r client.IndexResult, err error) {
						if err != nil {
							fmt.Fprintln(os.Stderr, err)
						} else if r.Ops > 0 {
							fmt.Printf("%s\tmembers=%d added=%d stale=%d discs=%d ops=%d\n", r.Collection, r.Members, r.AddedMembers, r.DroppedStale, r.Discs, r.Ops)
						}
					}})
					if err != nil {
						return err
					}
					fmt.Printf("collections: %d, changed: %d, archives stamped: %d, errors: %d\n", res.Collections, res.Changed, res.Archives, len(res.Errors))
					return nil
				},
			},
			{
				Name:  "restat",
				Usage: "Rebuild play-count / last-played statistics from the play events",
				Action: func(_ context.Context, c *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					n, err := tie.RecomputeStats()
					if err != nil {
						return err
					}
					fmt.Printf("subjects updated: %d\n", n)
					return nil
				},
			},
			{
				Name:  "schema",
				Usage: "Declare the media value types (year, duration, added-at, …)",
				Action: func(_ context.Context, c *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					return tie.EnsureMediaSchema()
				},
			},
		},
	}
}

func cmdHistory() *cli.Command {
	return &cli.Command{
		Name:  "history",
		Usage: "List recorded plays, newest first",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "since", Usage: "Only plays after: 72h, 30d or 2026-01-31"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 50, Usage: "Max plays (0 = all)"},
			&cli.BoolFlag{Name: "skips", Usage: "Include skipped plays"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
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
			events, err := tie.RecentPlays(q)
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
			if rows, err := tie.Expand(keys); err == nil {
				for _, r := range rows {
					n := client.RowFirst(r, "title")
					if a := client.RowFirst(r, "artist"); a != "" && n != "" {
						n = a + " — " + n
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

func cmdPlaylist() *cli.Command {
	return &cli.Command{
		Name:  "playlist",
		Usage: "Manage playlists: list, show, create, delete, migrate",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List playlists",
				Action: func(_ context.Context, c *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					ps, err := tie.ListPlaylists()
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					p, err := tie.ReadPlaylist(c.Args().First())
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					if c.Args().Len() < 1 {
						return errors.New("need a name")
					}
					uid, err := tie.CreatePlaylist(c.Args().First(), c.Args().Tail(), nil)
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
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					return tie.DeletePlaylist(c.Args().First())
				},
			},
			{
				Name:  "migrate",
				Usage: "Convert legacy (audio-dir) playlists to the playlist type",
				Action: func(_ context.Context, c *cli.Command) error {
					if tie == nil {
						return errors.New("Error: Config not loaded")
					}
					n, err := tie.MigrateLegacyPlaylists()
					if err != nil {
						return err
					}
					fmt.Printf("migrated: %d\n", n)
					return nil
				},
			},
		},
	}
}
