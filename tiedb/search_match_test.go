package tiedb

import "testing"

func TestFoldSearchText(t *testing.T) {
	cases := map[string]string{
		"Beyoncé":     "beyonce",
		"Don't":       "dont",
		"The Beatles": "the beatles",
		"  AC/DC  ":   "ac/dc",
		"Motörhead":   "motorhead",
		"日本語":         "日本語",
		"":            "",
	}
	for in, want := range cases {
		if got := foldSearchText(in); got != want {
			t.Errorf("foldSearchText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchTokens(t *testing.T) {
	cases := map[string][]string{
		"the beatles":    {"the", "beatles"},
		"don't stop":     {"dont", "stop"},
		"AC/DC":          {"ac", "dc"},
		"01 - come.flac": {"01", "come", "flac"},
		"日本語のうた":         {"日本語のうた"},
		"":               nil,
		"---":            nil,
	}
	for in, want := range cases {
		got := searchTokens(foldSearchText(in))
		if len(got) != len(want) {
			t.Errorf("searchTokens(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("searchTokens(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestEditDistanceWithin(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		dist int
		ok   bool
	}{
		{"beatles", "beetles", 1, 1, true},
		{"beatles", "beetles", 0, 0, false},
		{"abbey", "abey", 1, 1, true},
		{"abbey", "xyzzy", 2, 0, false},
		{"", "abc", 3, 3, true},
		{"abc", "", 3, 3, true},
	}
	for _, c := range cases {
		d, ok := editDistanceWithin(c.a, c.b, c.max)
		if ok != c.ok || (ok && d != c.dist) {
			t.Errorf("editDistanceWithin(%q,%q,%d) = (%d,%v), want (%d,%v)", c.a, c.b, c.max, d, ok, c.dist, c.ok)
		}
	}
}

func TestSearchRepresentativeFieldDeterministic(t *testing.T) {
	value := &searchValue{text: "x", norm: "x", tokens: []string{"x"}}
	fields := func(first, second string) []searchField {
		return []searchField{
			{relation: first, value: value},
			{relation: second, value: value},
		}
	}
	q := prepareSearchQuery(SearchQuery{Text: "x", Mode: SearchWords})

	// The forward association set is iterated from Go maps, so the field order
	// differs between two builds of the same logical subject. The representative
	// field (and therefore the rank tie-break) must not.
	first := &searchSubject{key: "a", fields: fields("b", "a")}
	second := &searchSubject{key: "z", fields: fields("a", "b")}
	if ok, _, field := q.match(first); !ok || first.fields[field].relation != "a" {
		t.Fatalf("first representative = %q (ok=%v), want a", first.fields[field].relation, ok)
	}
	if ok, _, field := q.match(second); !ok || second.fields[field].relation != "a" {
		t.Fatalf("second representative = %q (ok=%v), want a", second.fields[field].relation, ok)
	}

	matches, total, _ := rankCorpus(&searchCorpus{revision: 1, subjects: []*searchSubject{first, second}}, &q, 0, 10)
	if total != 2 || len(matches) != 2 {
		t.Fatalf("matches = %v (total %d), want 2", searchKeys(matches), total)
	}
	if matches[0].Key != "a" || matches[1].Key != "z" {
		t.Errorf("order = %v, want [a z] (key tie-break, not map order)", searchKeys(matches))
	}
}

func TestSearchPhraseRepresentativeDeterministic(t *testing.T) {
	value := &searchValue{text: "abbey road", norm: "abbey road", tokens: []string{"abbey", "road"}}
	fields := func(first, second string) []searchField {
		return []searchField{
			{relation: first, value: value},
			{relation: second, value: value},
		}
	}
	q := prepareSearchQuery(SearchQuery{Text: "abbey road", Mode: SearchWords})
	first := &searchSubject{key: "a", fields: fields("z", "a")}
	second := &searchSubject{key: "z", fields: fields("a", "z")}
	_, _, f1 := q.match(first)
	_, _, f2 := q.match(second)
	if first.fields[f1].relation != second.fields[f2].relation {
		t.Errorf("phrase representatives differ: %q vs %q", first.fields[f1].relation, second.fields[f2].relation)
	}
}

func TestPrepareSearchQuery(t *testing.T) {
	q := prepareSearchQuery(SearchQuery{Text: "  The   BEATLES  ", Mode: SearchWords})
	if q.raw != "the beatles" {
		t.Errorf("raw = %q", q.raw)
	}
	if len(q.tokens) != 2 || q.tokens[0] != "the" || q.tokens[1] != "beatles" {
		t.Errorf("tokens = %v", q.tokens)
	}
	if q.empty() {
		t.Error("query reported empty")
	}
	emptyQ := prepareSearchQuery(SearchQuery{Text: "!!!"})
	if !emptyQ.empty() {
		t.Error("punctuation-only query should be empty")
	}
}

func FuzzSearchMatcher(f *testing.F) {
	subject := &searchSubject{
		key: "k",
		fields: []searchField{
			{relation: "album", value: &searchValue{text: "Abbey Road", norm: "abbey road", tokens: []string{"abbey", "road"}}},
			{relation: "artist", value: &searchValue{text: "The Beatles", norm: "the beatles", tokens: []string{"the", "beatles"}}},
		},
	}
	for _, s := range []string{"", "abbey", "beatles abbey", "!!!", "日本語", "ａｂｂｅｙ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		q := prepareSearchQuery(SearchQuery{Text: text, Mode: SearchWords})
		_, _, _ = q.match(subject)
		q = prepareSearchQuery(SearchQuery{Text: text, Mode: SearchSubstring})
		_, _, _ = q.match(subject)
		q = prepareSearchQuery(SearchQuery{Text: text, Mode: SearchPrefix})
		_, _, _ = q.match(subject)
		q = prepareSearchQuery(SearchQuery{Text: text, Mode: SearchFuzzy})
		_, _, _ = q.match(subject)
	})
}
