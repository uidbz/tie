package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
)

func cmdAdd(a *app) *cli.Command {
	return &cli.Command{
		Name:    "add",
		Aliases: []string{"a"},
		Usage:   "Add a triple to the database: add KEY VALUE1 VALUE2",
		Action: func(_ context.Context, cCtx *cli.Command) error {
			if cCtx.Args().Len() < 3 {
				return exitError{code: exitUsage, msg: "add takes KEY VALUE1 VALUE2"}
			}
			args := cCtx.Args().Slice()

			if _, err := a.tie.Add(args[0], args[1], args[2]); err != nil {
				return operationalError("add: %v", err)
			}

			return nil
		},
	}

}

func cmdGet(a *app) *cli.Command {
	return &cli.Command{
		Name:    "get",
		Aliases: []string{"g"},
		Usage:   "Get triples: get KEY... [--not EXCLUDE...] (multiple keys intersect; - reads keys from stdin)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "reverse", Aliases: []string{"r"}},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 1000},
			&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0},
			&cli.StringFlag{Name: "sortby", Aliases: []string{"s"}},
			&cli.StringFlag{Name: "filter", Aliases: []string{"f"}},
			&cli.StringSliceFlag{Name: "not", Usage: "Exclude rows carrying this value1 (repeatable); a term spelled !VALUE does the same"},
			stdinFlag(),
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			args := ctx.Args().Slice()
			if len(args) < 1 && !ctx.Bool("stdin") {
				return exitError{code: exitUsage, msg: "get takes at least one KEY (or --stdin to read keys from stdin)"}
			}

			// Exclusion is a flag, not a leading dash: `-b` is eaten by flag
			// parsing, so the old spelling only worked behind `--` (§6.7). --not b
			// and !b both work; a bare -term still does for a script that already
			// writes `-- -term`.
			excluding := ctx.StringSlice("not")

			// Each query to run. Several KEYs on the command line mean the
			// reverse intersection they always meant; keys read from stdin are
			// asked one at a time, because `tie dump | cut -f1 | tie get -`
			// wants every key's rows, not the intersection of all of them (§6.7).
			var queries [][]string
			if wantsStdin(ctx) {
				if hasStdinArg(args) {
					warnDroppedArgs()
				}
				keys, err := readStdinKeys()
				if err != nil {
					return err
				}
				if len(keys) == 0 {
					fmt.Fprintln(os.Stderr, "no keys on stdin")
					return nil
				}
				for _, k := range keys {
					queries = append(queries, []string{k})
				}
				// Keys written alongside --stdin are asked the same way.
				for _, a := range args {
					if a != "-" {
						queries = append(queries, []string{a})
					}
				}
			} else {
				terms := []string{args[0]}
				for i := 1; i < len(args); i++ {
					switch {
					case strings.HasPrefix(args[i], "!"):
						excluding = append(excluding, strings.TrimPrefix(args[i], "!"))
					case strings.HasPrefix(args[i], "-") && len(args[i]) > 1:
						excluding = append(excluding, strings.TrimPrefix(args[i], "-"))
					default:
						terms = append(terms, args[i])
					}
				}
				queries = append(queries, terms)
			}

			o, err := a.outFor(ctx, "key", "value1", "value2")
			if err != nil {
				return err
			}
			var rows []client.Row
			for _, terms := range queries {
				found, _, err := a.tie.Query(client.QuerySpec{
					Terms:   terms,
					Exclude: excluding,
					Reverse: len(terms) > 1 || ctx.Bool("reverse"),
					Filter:  ctx.String("filter"),
					Offset:  ctx.Int("offset"),
					Limit:   ctx.Int("limit"),
					SortBy:  ctx.String("sortby"),
				})
				if err != nil {
					if errors.Is(err, client.ErrNotFound) {
						continue
					}
					return operationalError("get: %v", err)
				}
				rows = append(rows, found...)
			}
			return printRows(o, rows)
		},
	}
}

func cmdDel(a *app) *cli.Command {
	return &cli.Command{
		Name:    "del",
		Aliases: []string{"d"},
		Usage:   "Delete triples: del KEY VALUE1 VALUE2 | del KEY --relation VALUE1 | del KEY --all",
		Description: "Three shapes, chosen by flags rather than by `*`: one triple, every triple on one\n" +
			"relation, or every triple carrying a key. `*` is not a tie wildcard — the shell\n" +
			"expands it before tie sees it, so an unquoted `tie del key * *` in a non-empty\n" +
			"directory deletes (key, first-file, second-file) or nothing at all. Ask for what\n" +
			"you mean, with --relation or --all.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "all", Usage: "Delete every triple carrying KEY"},
			&cli.StringFlag{Name: "relation", Usage: "Delete every triple carrying KEY and this value1"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			all := ctx.Bool("all")
			relation := ctx.String("relation")
			if all && relation != "" {
				return exitError{code: exitUsage, msg: "--all and --relation are mutually exclusive"}
			}

			switch {
			case all, relation != "":
				if ctx.Args().Len() != 1 {
					return exitError{code: exitUsage, msg: "del KEY --all and del KEY --relation RELATION take only KEY"}
				}
				filter := relation
				if all {
					filter = ""
				}
				return deleteMatching(a, ctx.Args().First(), filter)

			case ctx.Args().Len() != 3:
				return exitError{code: exitUsage,
					msg: "del takes KEY VALUE1 VALUE2, or KEY with --all / --relation"}

			default:
				key, value1, value2 := ctx.Args().Get(0), ctx.Args().Get(1), ctx.Args().Get(2)
				if value1 == "*" || value2 == "*" {
					return exitError{code: exitUsage, msg: "`*` is a shell glob, not a tie wildcard: " +
						"use `tie del KEY --relation VALUE1` or `tie del KEY --all`"}
				}
				if _, err := a.tie.Delete(key, value1, value2); err != nil {
					return operationalError("del: %v", err)
				}
				return nil
			}
		},
	}
}

// deleteMatching removes every triple on key (and, when filter is set, on that
// value1). The rows are read first so the command can say how much it deleted,
// and a failure to query or to write is reported rather than swallowed: a
// silent exit 0 from a destructive command is the worst answer it can give.
func deleteMatching(a *app, key, filter string) error {
	rows, _, err := a.tie.Query(client.QuerySpec{
		Terms:  []string{key},
		Filter: filter,
		Limit:  -1,
	})
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "nothing to delete: no triples carry %s\n", key)
			return nil
		}
		return operationalError("del: %v", err)
	}

	b := a.tie.NewBatch()
	triples := 0
	for _, r := range rows {
		for relation, values := range r.Attributes {
			for _, v := range values {
				b.Delete(r.Key, relation, v)
				triples++
			}
		}
	}
	if _, err := a.tie.Batch(b); err != nil {
		return operationalError("del: %v", err)
	}
	fmt.Fprintf(os.Stderr, "deleted %s\n", pluralWith(triples, "triple", "triples"))
	return nil
}

func printRows(o *out, rows []client.Row) error {
	records := make([][]string, 0, len(rows))
	for _, r := range rows {
		relations := make([]string, 0, len(r.Attributes))
		for relation := range r.Attributes {
			relations = append(relations, relation)
		}
		sort.Strings(relations)
		for _, relation := range relations {
			for _, v := range r.Attributes[relation] {
				records = append(records, []string{r.Key, relation, v})
			}
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i][0] != records[j][0] {
			return records[i][0] < records[j][0]
		}
		return records[i][1] < records[j][1]
	})
	for _, rec := range records {
		o.row(rec...)
	}
	return o.flush()
}

// resolveFileHost picks the filehost for a file command. A raw --server address
// takes precedence (with --insecure controlling TLS verification); otherwise the
// named --host is looked up in config.
