package compose

import (
	"strings"
	"testing"
)

// Review B9k: As renames exactly one file. Spec{Paths: [a, b], As: "x"}
// used to reach selectEntries and rename BOTH selections to "x", surfacing as
// a confusing collision. The shared entry points must reject the spec up
// front, naming it, so the runner and direct API callers get the same clear
// error the Risor layer gives at As().

func TestResolveStepsRejectsAsWithMultiplePaths(t *testing.T) {
	lookup := func(step string) (string, bool) { return "h1", true }
	_, err := ResolveSteps([]Spec{
		{FromStep: "mist", Paths: []string{"a.tsv", "b.tsv"}, As: "renamed.tsv"},
	}, lookup)
	if err == nil {
		t.Fatal("ResolveSteps accepted As with two paths")
	}
	if !strings.Contains(err.Error(), "step mist") || !strings.Contains(err.Error(), "As(\"renamed.tsv\")") {
		t.Errorf("error %q does not name the spec and its As", err)
	}
	if !strings.Contains(err.Error(), "2 paths") {
		t.Errorf("error %q does not report how many paths were selected", err)
	}
}

func TestComposeRejectsAsWithMultiplePaths(t *testing.T) {
	// The runner calls Compose directly, without ResolveSteps, so the guard
	// must live there too. Validation fires before any fetch, so the bogus
	// filehost is never contacted.
	_, err := Build("http://127.0.0.1:1", "", []Spec{
		{Hash: "abc", Paths: []string{"a.tsv", "b.tsv"}, As: "x.tsv"},
	})
	if err == nil {
		t.Fatal("Compose accepted As with two paths")
	}
	if !strings.Contains(err.Error(), "tree abc") || !strings.Contains(err.Error(), "As(\"x.tsv\")") {
		t.Errorf("error %q does not name the spec and its As", err)
	}
}

// As with exactly one path stays legal, and a spec with several paths and no
// As stays legal: only the combination is rejected.
func TestAsWithSinglePathStillValid(t *testing.T) {
	lookup := func(step string) (string, bool) { return "h1", true }
	out, err := ResolveSteps([]Spec{
		{FromStep: "mist", Paths: []string{"a.tsv"}, As: "x.tsv"},
		{FromStep: "mist", Paths: []string{"a.tsv", "b.tsv"}},
		{FromStep: "mist", Glob: "*.tsv", As: "x.tsv"}, // glob+As rejected at the Risor layer, not here
	}, lookup)
	if err != nil {
		t.Fatalf("valid specs rejected: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d specs, want 3", len(out))
	}
}
