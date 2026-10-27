package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/tiedb"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

// cmdSearch runs the free-text search request. Unlike `get`, it does not take
// an implicit relation vocabulary: the caller names the relations to search so
// a typo cannot silently scan (and rank) the whole metadata set.
func cmdSearch(a *app) *cli.Command {
	return &cli.Command{
		Name:  "search",
		Usage: "Free-text search over named relations: search QUERY --relation REL... [--type TYPE...]",
		Description: "Relations are required and repeatable. Matching is case-insensitive and accent-folded.\n" +
			"The default mode is `words`: every query token must match somewhere on the subject, so\n" +
			"`beatles abbey` finds an album whose artist is The Beatles and whose title is Abbey Road.\n" +
			"Scoping with --type narrows candidates through the reverse tie-type index, which is what\n" +
			"keeps a search over audio files from walking the rest of the collection.\n\n" +
			"Output columns are key, relation, value and score. No matches is an empty result (exit 0).",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "relation", Aliases: []string{"r"}, Usage: "Searchable relation (repeatable; required)"},
			&cli.StringSliceFlag{Name: "type", Aliases: []string{"t"}, Usage: "tie-type scope (repeatable; empty = all types)"},
			&cli.StringFlag{Name: "mode", Usage: "Match mode: words, substring, prefix or fuzzy (default: words)"},
			&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0, Usage: "Skip this many ranked matches"},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: clientSearchDefaultLimit, Usage: "Maximum matches to return (max 1000)"},
			&cli.StringSliceFlag{Name: "weight", Aliases: []string{"w"}, Usage: "Relation weight as RELATION=PERCENT (repeatable; 100 = neutral)"},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			args := ctx.Args().Slice()
			if len(args) != 1 {
				return exitError{code: exitUsage, msg: "search takes exactly one QUERY"}
			}
			relations := ctx.StringSlice("relation")
			if len(relations) == 0 {
				return exitError{code: exitUsage, msg: "search needs at least one --relation"}
			}
			mode, err := parseSearchMode(ctx.String("mode"))
			if err != nil {
				return exitError{code: exitUsage, msg: err.Error()}
			}
			weights, err := parseSearchWeights(ctx.StringSlice("weight"))
			if err != nil {
				return exitError{code: exitUsage, msg: err.Error()}
			}
			types, err := searchTypes(ctx.StringSlice("type"))
			if err != nil {
				return exitError{code: exitUsage, msg: err.Error()}
			}

			spec := client.SearchSpec{
				Query:     args[0],
				Relations: searchRelations(relations),
				TieTypes:  types,
				Mode:      mode,
				Weights:   weights,
				Offset:    ctx.Int("offset"),
				Limit:     ctx.Int("limit"),
			}
			matches, total, err := a.tie.Search(spec)
			if err != nil {
				return operationalError("search: %v", err)
			}

			o, err := a.outFor(ctx, "key", "relation", "value", "score")
			if err != nil {
				return err
			}
			for _, m := range matches {
				o.row(m.Key, string(m.Relation), m.Value, strconv.Itoa(m.Score))
			}
			if err := o.flush(); err != nil {
				return err
			}
			// A no-match search is a normal result; only print the count note
			// when there is more than one page, so shell pipelines stay clean.
			if total > len(matches) && term.IsTerminal(int(os.Stderr.Fd())) {
				fmt.Fprintf(os.Stderr, "%d matches (showing %d)\n", total, len(matches))
			}
			return nil
		},
	}
}

const clientSearchDefaultLimit = 100

func parseSearchMode(s string) (tiedb.SearchMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(tiedb.SearchWords):
		return tiedb.SearchWords, nil
	case string(tiedb.SearchSubstring):
		return tiedb.SearchSubstring, nil
	case string(tiedb.SearchPrefix):
		return tiedb.SearchPrefix, nil
	case string(tiedb.SearchFuzzy):
		return tiedb.SearchFuzzy, nil
	default:
		return "", fmt.Errorf("unknown search mode %q: use words, substring, prefix or fuzzy", s)
	}
}

func parseSearchWeights(values []string) (map[client.Relation]int, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[client.Relation]int, len(values))
	for _, v := range values {
		name, pct, ok := strings.Cut(v, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("--weight %q: use RELATION=PERCENT", v)
		}
		n, err := strconv.Atoi(strings.TrimSpace(pct))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("--weight %q: percent must be a positive integer", v)
		}
		out[client.Relation(name)] = n
	}
	return out, nil
}

func searchRelations(values []string) []client.Relation {
	out := make([]client.Relation, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, client.Relation(v))
		}
	}
	return out
}

func searchTypes(values []string) ([]client.TieType, error) {
	out := make([]client.TieType, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		t := client.StringToTieType(v)
		if t.String() != v {
			return nil, fmt.Errorf("unknown tie-type %q: use a built-in type such as audio-file, audio-dir, directory or file", v)
		}
		out = append(out, t)
	}
	return out, nil
}
