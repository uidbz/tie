package main

import (
	"context"
	"errors"
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
			"Scoping with --type (a built-in tie-type or a registered dir-type label) narrows candidates\n" +
			"through the reverse tie-type index, which is what keeps a search over audio files from walking\n" +
			"the rest of the collection.\n\n" +
			"Output columns are key, relation, value and score. No matches is an empty result (exit 0).",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "relation", Aliases: []string{"r"}, Usage: "Searchable relation (repeatable; required)"},
			&cli.StringSliceFlag{Name: "type", Aliases: []string{"t"}, Usage: "tie-type scope: a built-in type or a registered dir-type (repeatable; empty = all types)"},
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
			types, err := searchTypes(a.tie, ctx.StringSlice("type"))
			if err != nil {
				var unknown unknownTieTypeError
				if errors.As(err, &unknown) {
					return exitError{code: exitUsage, msg: err.Error()}
				}
				return operationalError("search: %v", err)
			}

			spec := client.SearchSpec{
				Query:        args[0],
				Relations:    searchRelations(relations),
				TieTypeNames: types,
				Mode:         mode,
				Weights:      weights,
				Offset:       ctx.Int("offset"),
				Limit:        ctx.Int("limit"),
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

// unknownTieTypeError is a usage error: the name is neither a built-in
// TieType nor a registered dir-type. It is a distinct type so the command can
// keep exit 2 for a typo while a failure to read the registry exits 4.
type unknownTieTypeError struct{ name string }

func (e unknownTieTypeError) Error() string {
	return fmt.Sprintf("unknown tie-type %q: use a built-in type or a registered dir-type", e.name)
}

// searchTypes accepts built-in TieType names and custom dir-type labels from
// the store's registry. Built-ins need no round trip; only an unknown name
// asks ListDirTypes, so a typo is still rejected instead of silently matching
// nothing (StringToTieType collapses an unknown name to unknown-file).
func searchTypes(tie *client.TieClient, values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	unknown := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if client.IsBuiltinTieType(v) {
			out = append(out, v)
			continue
		}
		unknown = append(unknown, v)
	}
	if len(unknown) == 0 {
		return out, nil
	}

	known, err := tie.ListDirTypes()
	if err != nil {
		return nil, fmt.Errorf("listing dir-types: %w", err)
	}
	set := make(map[string]bool, len(known))
	for _, name := range known {
		set[name] = true
	}
	for _, name := range unknown {
		if !set[name] {
			return nil, unknownTieTypeError{name: name}
		}
		out = append(out, name)
	}
	return out, nil
}
