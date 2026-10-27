package main

import (
	"context"
	"errors"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/testenv"
	"github.com/urfave/cli/v3"
)

func searchTestApp(t *testing.T) *app {
	t.Helper()
	env, stop, err := testenv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	cfg := client.Config{
		TripleStoreURL: env.TripleStoreURL,
		Namespace:      "Collections",
		Collection:     "Main",
	}
	a := &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}
	b := a.tie.NewBatch()
	b.Add("clisearch-album", "tie-type", "audio-dir")
	b.Add("clisearch-album", "album", "Abbey Road")
	b.Add("clisearch-album", "album-artist", "The Beatles")
	if _, err := a.tie.Batch(b); err != nil {
		t.Fatal(err)
	}
	return a
}

func runSearch(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		root := &cli.Command{Name: "tie", Commands: []*cli.Command{cmdSearch(a)}}
		return root.Run(context.Background(), append([]string{"tie", "search"}, args...))
	})
}

func TestSearchCommandPrintsRankedTSV(t *testing.T) {
	a := searchTestApp(t)

	out, err := runSearch(t, a, "abbey", "--relation", "album", "--type", "audio-dir")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	want := "clisearch-album\talbum\tAbbey Road\t"
	if len(out) < len(want) || out[:len(want)] != want {
		t.Errorf("search output = %q, want prefix %q", out, want)
	}
}

func TestSearchCommandRequiresRelation(t *testing.T) {
	a := searchTestApp(t)

	_, err := runSearch(t, a, "abbey")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != exitUsage {
		t.Fatalf("search without --relation: err = %v, want exit-2", err)
	}
}

func TestSearchCommandBadMode(t *testing.T) {
	a := searchTestApp(t)

	_, err := runSearch(t, a, "abbey", "--relation", "album", "--mode", "nope")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != exitUsage {
		t.Fatalf("bad mode: err = %v, want exit-2", err)
	}
}

func TestSearchCommandNoMatchesIsEmptySuccess(t *testing.T) {
	a := searchTestApp(t)

	out, err := runSearch(t, a, "zzzz-no-such", "--relation", "album")
	if err != nil {
		t.Fatalf("no-match search returned an error: %v", err)
	}
	if out != "" {
		t.Errorf("no-match output = %q, want empty", out)
	}
}
