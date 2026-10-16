package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/uidbz/tie/client"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
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
					fmt.Println("Error:", err.Error())
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
				fmt.Println("Function to delete specific value2 from all value1's is not implemented, because it is most likely a typo.")
				fmt.Println("Did you mean: tie delete", args[0], args[2], "* (delete all value2's from specific key + value1 pair?)")

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
