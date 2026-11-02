package compose

import (
	"errors"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// hashOf fabricates a distinct 64-char hex address per label, so tests can tell
// entries apart without hashing real content.
func hashOf(label string) string {
	h := strings.Repeat("0", 64-len(label)) + label
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("0123456789abcdef", r) {
			return r
		}
		return 'a'
	}, h)
}

func file(name, parent string) *DirItem {
	p := name
	if parent != "" {
		p = parent + "/" + name
	}
	return &DirItem{Name: name, Path: p, MediaType: "file", Hash: hashOf(name), Size: len(name)}
}

func dir(name, parent string, children ...*DirItem) *DirItem {
	p := name
	if parent != "" {
		p = parent + "/" + name
	}
	return &DirItem{
		Name: name, Path: p, MediaType: "dir", Hash: hashOf(name),
		Head: []byte(metadata.DirHeader), Children: children,
	}
}

func root(children ...*DirItem) *sourceTree {
	return indexTree(&DirItem{MediaType: "dir", Children: children})
}

func names(sel []selected) []string {
	out := make([]string, 0, len(sel))
	for _, s := range sel {
		out = append(out, s.Name)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFlattensToBasename(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": root(dir("results", "", file("all_data.csv", "results"))),
	}
	sel, err := selectEntries(roots, []Spec{
		{Hash: "h1", Paths: []string{"results/all_data.csv"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"all_data.csv"})
}

func TestAsRenames(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": root(dir("retrieval", "", file("x_mist_cosine.tsv", "retrieval"))),
	}
	sel, err := selectEntries(roots, []Spec{
		{Hash: "h1", Paths: []string{"retrieval/x_mist_cosine.tsv"}, As: "x_mist_pbc_cosine.tsv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"x_mist_pbc_cosine.tsv"})
}

// The rename is what keeps two sources' identically-named files apart once
// flattened -- the job the io.cp calls in pipeline.risor used to do.
func TestAsAvoidsCollisionBetweenSources(t *testing.T) {
	shared := "retrieval/r_mist_cosine.tsv"
	mist := root(dir("retrieval", "", file("r_mist_cosine.tsv", "retrieval")))
	pbc := root(dir("retrieval", "", &DirItem{
		Name: "r_mist_cosine.tsv", Path: "retrieval/r_mist_cosine.tsv",
		MediaType: "file", Hash: hashOf("beef"), Size: 9,
	}))
	roots := map[string]*sourceTree{"mist": mist, "pbc": pbc}

	if _, err := selectEntries(roots, []Spec{
		{Hash: "mist", FromStep: "mist", Paths: []string{shared}},
		{Hash: "pbc", FromStep: "mist_pbc", Paths: []string{shared}},
	}); err == nil {
		t.Fatal("expected a collision error")
	} else if !strings.Contains(err.Error(), "collision") || !strings.Contains(err.Error(), ".As()") {
		t.Fatalf("unhelpful collision error: %v", err)
	}

	sel, err := selectEntries(roots, []Spec{
		{Hash: "mist", FromStep: "mist", Paths: []string{shared}},
		{Hash: "pbc", FromStep: "mist_pbc", Paths: []string{shared}, As: "r_mist_pbc_cosine.tsv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"r_mist_cosine.tsv", "r_mist_pbc_cosine.tsv"})
}

func TestSameHashDedupes(t *testing.T) {
	roots := map[string]*sourceTree{"h1": root(file("a.tsv", ""))}
	sel, err := selectEntries(roots, []Spec{
		{Hash: "h1", Paths: []string{"a.tsv"}},
		{Hash: "h1", Paths: []string{"a.tsv"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"a.tsv"})
}

func TestGlobIsNonRecursiveAndFilesOnly(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": root(
			file("a.tsv", ""),
			file("b.tsv", ""),
			file("c.csv", ""),
			dir("nested.tsv", "", file("deep.tsv", "nested.tsv")),
		),
	}
	sel, err := selectEntries(roots, []Spec{{Hash: "h1", Glob: "*.tsv"}})
	if err != nil {
		t.Fatal(err)
	}
	// c.csv excluded by pattern; nested.tsv excluded for being a directory;
	// deep.tsv excluded because the match does not recurse.
	eq(t, names(sel), []string{"a.tsv", "b.tsv"})
}

func TestGlobWithDirectoryPrefix(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": root(
			file("top.tsv", ""),
			dir("retrieval", "", file("r1_mist_cosine.tsv", "retrieval"), file("other.csv", "retrieval")),
		),
	}
	sel, err := selectEntries(roots, []Spec{{Hash: "h1", Glob: "retrieval/*_mist_cosine.tsv"}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"r1_mist_cosine.tsv"})
}

func TestMissingPathIsStrictByDefault(t *testing.T) {
	roots := map[string]*sourceTree{"h1": root(file("a.tsv", ""))}
	_, err := selectEntries(roots, []Spec{
		{Hash: "h1", FromStep: "msbuddy", Paths: []string{"nope.tsv"}},
	})
	if err == nil {
		t.Fatal("expected a strict error for a missing path")
	}
	if !strings.Contains(err.Error(), "nope.tsv") || !strings.Contains(err.Error(), "msbuddy") {
		t.Fatalf("error should name the path and the source: %v", err)
	}
}

func TestOptionalSkipsMissing(t *testing.T) {
	roots := map[string]*sourceTree{"h1": root(file("a.tsv", ""))}
	sel, err := selectEntries(roots, []Spec{
		{Hash: "h1", Paths: []string{"a.tsv", "nope.tsv"}, Optional: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"a.tsv"})
}

func TestGlobMatchingNothingIsStrictUnlessOptional(t *testing.T) {
	roots := map[string]*sourceTree{"h1": root(file("a.csv", ""))}
	if _, err := selectEntries(roots, []Spec{{Hash: "h1", Glob: "*.tsv"}}); err == nil {
		t.Fatal("expected an error when a glob matches nothing")
	}
	sel, err := selectEntries(roots, []Spec{{Hash: "h1", Glob: "*.tsv", Optional: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 0 {
		t.Fatalf("expected nothing selected, got %v", names(sel))
	}
}

func TestWholeTreeSplicesChildrenPreservingNesting(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": root(file("a.csv", ""), dir("sub", "", file("b.csv", "sub"))),
	}
	sel, err := selectEntries(roots, []Spec{{Hash: "h1"}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, names(sel), []string{"a.csv", "sub"})
	for _, s := range sel {
		if s.Name == "sub" && !metadata.IsDirHead(s.Entry.Head) {
			t.Fatal("subdirectory lost its directory head, so nesting would be flattened")
		}
	}
}

// Entries carry the source manifest's Hash/Size/Head through unchanged. That is
// what makes a composed manifest byte-identical to one putlib uploaded, and so
// makes the composed input hash equal UploadBatch's for the same file set.
func TestEntryFieldsAreCarriedThrough(t *testing.T) {
	src := &DirItem{
		Name: "a.tsv", Path: "a.tsv", MediaType: "file",
		Hash: hashOf("dad"), Size: 4242, Head: []byte("col1\tcol2\n"),
	}
	roots := map[string]*sourceTree{"h1": root(src)}
	sel, err := selectEntries(roots, []Spec{{Hash: "h1", Paths: []string{"a.tsv"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := sel[0].Entry
	if got.Hash != src.Hash || got.Size != src.Size || string(got.Head) != string(src.Head) {
		t.Fatalf("entry not carried through: %+v vs %+v", got, src)
	}
}

func TestFileSourceIsRejected(t *testing.T) {
	roots := map[string]*sourceTree{
		"h1": indexTree(&DirItem{MediaType: "file", Hash: hashOf("f")}),
	}
	if _, err := selectEntries(roots, []Spec{{Hash: "h1"}}); err == nil {
		t.Fatal("expected an error when the source tree is a file")
	}
}

func TestResolveStepsDropsOptionalAndErrorsOnRequired(t *testing.T) {
	recorded := map[string]string{"mzmine": hashOf("abc")}
	lookup := func(step string) (string, bool) {
		h, ok := recorded[step]
		return h, ok
	}

	got, err := ResolveSteps([]Spec{
		{FromStep: "mzmine", Paths: []string{"a.csv"}},
		{FromStep: "never_ran", Paths: []string{"b.csv"}, Optional: true},
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hash != hashOf("abc") {
		t.Fatalf("expected the optional unresolved spec dropped and the other resolved, got %+v", got)
	}

	_, err = ResolveSteps([]Spec{{FromStep: "never_ran", Paths: []string{"b.csv"}}}, lookup)
	var unresolved *UnresolvedStepError
	if !errors.As(err, &unresolved) {
		t.Fatalf("expected an UnresolvedStepError, got %v", err)
	}
	if unresolved.Step != "never_ran" {
		t.Fatalf("error names the wrong step: %q", unresolved.Step)
	}
}

// Dependencies is what a pipeline derives its step graph from, so a step can
// never declare a dependency that its selection does not actually create.
func TestDependenciesAreRequiredUnlessEverySpecIsOptional(t *testing.T) {
	deps := Dependencies([]Spec{
		{FromStep: "mzmine", Paths: []string{"a.csv"}},
		{FromStep: "mist", Paths: []string{"b.tsv"}, Optional: true},
		// A second, required selection from mist promotes it back to required.
		{FromStep: "mist", Paths: []string{"c.tsv"}},
		{FromStep: "ms2lda", Glob: "*.tsv", Optional: true},
		{Hash: hashOf("seed"), Paths: []string{"d.csv"}}, // no step: not an edge
	})

	want := []Dependency{
		{Step: "mzmine", Required: true},
		{Step: "mist", Required: true},
		{Step: "ms2lda", Required: false},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %+v, want %+v", deps, want)
	}
	for i := range want {
		if deps[i] != want[i] {
			t.Errorf("dep %d: got %+v, want %+v", i, deps[i], want[i])
		}
	}
}
