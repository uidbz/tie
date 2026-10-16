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
		Usage:   "Add a triple to the database: add [key] [value1] [value2]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "gallery-name", Aliases: []string{"g"}},
			&cli.StringFlag{Name: "tags", Aliases: []string{"t"}},
		},

		Action: func(_ context.Context, cCtx *cli.Command) error {
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()

			if _, err := a.tie.Add(args[0], args[1], args[2]); err != nil {
				return errors.New("Add Error: " + err.Error())
			}

			return nil
		},
	}

}

func cmdGet(a *app) *cli.Command {
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
			rows, _, err := a.tie.Query(client.QuerySpec{
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
			o, err := a.outFor(ctx, "key", "value1", "value2")
			if err != nil {
				return err
			}
			if err := printRows(o, rows); err != nil {
				return errors.New("Get Error: " + err.Error())
			}

			return nil
		},
	}
}

// printRows writes query results to stdout, flattening each Row back into
// key<TAB>relation<TAB>value lines. When stdout is an interactive terminal it
// renders aligned, headered columns for readability; when stdout is piped or
// redirected it emits plain tab-separated lines so downstream tools like
// cut/awk/sort keep working unchanged.

func cmdDel(a *app) *cli.Command {
	return &cli.Command{
		Name:    "del",
		Aliases: []string{"d"},
		Usage:   "Delete a triple: del [key] [value1] [value2]",
		Action: func(_ context.Context, cCtx *cli.Command) error {
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()
			deleteBatch := func(filter string) error {
				rows, _, err := a.tie.Query(client.QuerySpec{
					Terms:  []string{args[0]},
					Filter: filter,
					Limit:  -1,
				})
				if err != nil {
					if errors.Is(err, client.ErrNotFound) {
						return nil
					}
					fmt.Fprintln(os.Stderr, "Error:", err.Error())
					return nil
				}
				b := a.tie.NewBatch()
				for _, r := range rows {
					for relation, values := range r.Attributes {
						for _, v := range values {
							b.Delete(r.Key, relation, v)
						}
					}
				}
				if _, err := a.tie.Batch(b); err != nil {
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
				fmt.Fprintln(os.Stderr, "Function to delete specific value2 from all value1's is not implemented, because it is most likely a typo.")
				fmt.Fprintf(os.Stderr, "Did you mean: tie delete %s %s * (delete all value2's from specific key + value1 pair?)\n", args[0], args[2])

			default:
				if _, err := a.tie.Delete(args[0], args[1], args[2]); err != nil {
					return errors.New("Delete Error: " + err.Error())
				}

			}

			return nil
		},
	}
}

// cmdVersion prints the client build and, when a config is loaded, the build
// each configured server is running — the quick way to confirm a deploy landed
// on a remote triplestore/filehost. Servers that cannot be reached are
// reported inline rather than failing the command.

// printRows writes query results to stdout, flattening each Row back into
// key<TAB>relation<TAB>value lines. When stdout is an interactive terminal it
// renders aligned, headered columns for readability; when stdout is piped or
// redirected it emits plain tab-separated lines so downstream tools like
// cut/awk/sort keep working unchanged.
// printRows writes the rows of a get/query through the shared renderer, in a
// fixed order: by key, then relation. The relations of a row live in a map, so
// the old code printed the same query in a different order on every run, which
// broke diff, comm and golden tests (§6.4). Values keep the order the store
// returned them in.
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
