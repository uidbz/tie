package tiedb

// Free-text matching: folding, tokenisation, per-token scoring and the bounded
// edit distance used by fuzzy search.
//
// Matching is deliberately subject-level, not value-level: a query may be
// satisfied by different fields of one subject (artist "The Beatles", album
// "Abbey Road" for the query "beatles abbey"). The corpus stores one entry per
// subject and its searchable fields; this file decides whether that entry
// matches and how strongly.
//
// Scores are integers so that two runs of the same query produce byte-identical
// ordering. They are not a calibrated probability; only the relative order
// matters.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// foldSearchText lower-cases text, strips combining marks (so "Beyoncé" and
// "beyonce" compare equal), drops apostrophes (so "Don't" tokenises as
// "dont") and collapses whitespace runs to one space. Everything else is
// preserved; tokenisation handles separators.
func foldSearchText(s string) string {
	if s == "" {
		return ""
	}
	s = norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r == '\'' || r == '\u2019' || r == '\u02bc':
			continue
		case unicode.IsSpace(r):
			if b.Len() > 0 && !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		default:
			space = false
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// searchTokens splits folded text into letter/digit runs. Non-letter,
// non-digit runes are separators. Tokens are allocated strings rather than
// substrings of the folded value because apostrophes are removed inside a run
// ("don't" -> "dont"), which slicing cannot express.
func searchTokens(s string) []string {
	if s == "" {
		return nil
	}
	toks := make([]string, 0, 4)
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		toks = append(toks, b.String())
		b.Reset()
	}
	for _, r := range s {
		if r == '\'' || r == '\u2019' || r == '\u02bc' {
			continue // keep "don't" one token even on unfolded input
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return toks
}

// preparedSearchQuery is one request's query text, folded and tokenised once.
type preparedSearchQuery struct {
	mode    SearchMode
	raw     string   // folded, whitespace-collapsed full query
	tokens  []string // folded tokens, de-duplicated, order preserved
	weights map[string]int

	// fieldScores is scratch owned by one Search call. matchTokens reuses it
	// for every subject, so a warm query does not allocate a score slice per
	// subject. It is not safe to use one preparedSearchQuery concurrently.
	fieldScores []int
}

func prepareSearchQuery(q SearchQuery) preparedSearchQuery {
	raw := foldSearchText(q.Text)
	raw = strings.Join(strings.Fields(raw), " ")
	p := preparedSearchQuery{mode: q.Mode, raw: raw, weights: q.Weights}
	if p.mode == SearchSubstring {
		return p
	}
	seen := make(map[string]bool)
	for _, t := range searchTokens(raw) {
		if seen[t] {
			continue
		}
		seen[t] = true
		p.tokens = append(p.tokens, t)
		if len(p.tokens) == maxSearchTokens {
			break
		}
	}
	return p
}

// empty reports whether the query has nothing to match on after folding (for
// example a query of only punctuation).
func (q *preparedSearchQuery) empty() bool {
	if q.raw == "" {
		return true
	}
	if q.mode == SearchSubstring {
		return false
	}
	return len(q.tokens) == 0
}

// match scores one subject. ok is false when the query is not satisfied; field
// is the index of the field that contributed most to the score (used as the
// representative relation/value of the hit).
func (q *preparedSearchQuery) match(s *searchSubject) (ok bool, score int, field int) {
	if q.mode == SearchSubstring {
		return q.matchSubstring(s)
	}
	return q.matchTokens(s)
}

func (q *preparedSearchQuery) matchSubstring(s *searchSubject) (bool, int, int) {
	best, bestField := -1, -1
	for i := range s.fields {
		f := &s.fields[i]
		text := f.value.norm
		if !strings.Contains(text, q.raw) {
			continue
		}
		sc := substringScore(text, q.raw)
		sc = weightedScore(sc, f.relation, q.weights)
		sc += lengthBonus(text, q.raw)
		if betterSearchField(sc, f, best, fieldAt(s, bestField)) {
			best, bestField = sc, i
		}
	}
	if bestField < 0 {
		return false, 0, -1
	}
	return true, best, bestField
}

// betterSearchField reports whether candidate (with score) should replace the
// current best field. A higher score always wins; equal scores tie-break by
// relation name, then value text. The tie-break matters because the forward
// association set is iterated from Go maps: without it, a subject with two
// equally-scoring fields can report a different representative relation after
// a corpus rebuild, and rankCorpus uses that relation as its ordering key.
func betterSearchField(score int, candidate *searchField, bestScore int, best *searchField) bool {
	if best == nil || score > bestScore {
		return true
	}
	if score < bestScore {
		return false
	}
	if candidate.relation != best.relation {
		return candidate.relation < best.relation
	}
	return candidate.value.text < best.value.text
}

// fieldAt returns s.fields[i], or nil for the "no field chosen yet" index.
func fieldAt(s *searchSubject, i int) *searchField {
	if i < 0 || i >= len(s.fields) {
		return nil
	}
	return &s.fields[i]
}

func (q *preparedSearchQuery) matchTokens(s *searchSubject) (bool, int, int) {
	if cap(q.fieldScores) < len(s.fields) {
		q.fieldScores = make([]int, len(s.fields))
	} else {
		q.fieldScores = q.fieldScores[:len(s.fields)]
		clear(q.fieldScores)
	}
	perField := q.fieldScores
	total := 0
	for _, token := range q.tokens {
		best, bestField := -1, -1
		for i := range s.fields {
			f := &s.fields[i]
			sc, ok := tokenScore(f.value, token, q.mode)
			if !ok {
				continue
			}
			sc = weightedScore(sc, f.relation, q.weights)
			if betterSearchField(sc, f, best, fieldAt(s, bestField)) {
				best, bestField = sc, i
			}
		}
		if bestField < 0 {
			return false, 0, -1
		}
		perField[bestField] += best
		total += best
	}

	// Phrase bonus: a field containing the complete folded query is a much
	// stronger signal than the same tokens scattered across fields. It also
	// makes the phrase field the representative field. When several fields
	// contain the phrase, pick deterministically rather than by map order.
	if len(q.tokens) > 1 {
		const phraseBonus = 250
		phraseField := -1
		for i := range s.fields {
			if !strings.Contains(s.fields[i].value.norm, q.raw) {
				continue
			}
			if phraseField < 0 || betterSearchField(perField[i], &s.fields[i], perField[phraseField], fieldAt(s, phraseField)) {
				phraseField = i
			}
		}
		if phraseField >= 0 {
			perField[phraseField] += phraseBonus
			total += phraseBonus
		}
	}

	bestField := -1
	for i, c := range perField {
		if bestField < 0 || betterSearchField(c, &s.fields[i], perField[bestField], fieldAt(s, bestField)) {
			bestField = i
		}
	}
	if bestField < 0 || perField[bestField] <= 0 {
		return false, 0, -1
	}
	total += lengthBonus(s.fields[bestField].value.norm, q.raw)
	return true, total, bestField
}

// tokenScore returns the best score for one query token anywhere in value. It
// is the hot loop of a warm search: it walks the value's pre-tokenised slice
// and only falls back to the full normalized string or fuzzy distance when no
// token-level match exists.
func tokenScore(v *searchValue, token string, mode SearchMode) (int, bool) {
	best := 0
	for _, tok := range v.tokens {
		switch {
		case tok == token:
			return 800, true
		case strings.HasPrefix(tok, token):
			if best < 700 {
				best = 700
			}
		case mode != SearchPrefix && strings.Contains(tok, token):
			if best < 600 {
				best = 600
			}
		}
	}
	if best == 0 && mode != SearchPrefix && strings.Contains(v.norm, token) {
		// A token can span two candidate tokens ("abbeyroad" in "abbey road"),
		// which the token walk cannot see.
		best = 500
	}
	if best == 0 && mode == SearchPrefix && strings.HasPrefix(v.norm, token) {
		best = 700
	}
	if best == 0 && mode == SearchFuzzy {
		if max := fuzzyDistanceFor(token); max > 0 {
			for _, tok := range v.tokens {
				if len(tok) > maxFuzzyCandidateBytes {
					continue
				}
				if d, ok := editDistanceWithin(token, tok, max); ok {
					if sc := 600 - 100*d; sc > best {
						best = sc
					}
				}
			}
		}
	}
	return best, best > 0
}

// substringScore rates a whole-query substring hit: exact field, prefix,
// word-boundary prefix, then anywhere.
func substringScore(text, raw string) int {
	if text == raw {
		return 1000
	}
	idx := strings.Index(text, raw)
	if idx < 0 {
		return 0
	}
	if idx == 0 {
		return 850
	}
	if isWordBoundary(text, idx) {
		return 800
	}
	return 600
}

// isWordBoundary reports whether the byte offset i in s starts a word: the
// preceding rune is not a letter or digit.
func isWordBoundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// lengthBonus mildly prefers shorter values: "Rock" should outrank a long
// album title that merely begins with the same query.
func lengthBonus(text, raw string) int {
	extra := len(text) - len(raw)
	if extra <= 0 {
		return 50
	}
	b := 50 - extra/2
	if b < 0 {
		return 0
	}
	return b
}

// weightedScore applies the request's per-relation weight (percent). Relation
// weights let an app say a title match outranks a filename match without
// changing the generic matcher.
func weightedScore(score int, relation string, weights map[string]int) int {
	w := weights[relation]
	if w <= 0 {
		w = 100
	}
	return score * w / 100
}

// maxFuzzyCandidateBytes bounds a candidate token compared by the fuzzy
// matcher. It also sizes the stack buffers in editDistanceWithin; a longer
// candidate falls back to heap buffers.
const maxFuzzyCandidateBytes = 64

// fuzzyDistanceFor caps edit distance by query-token length: short tokens need
// an exact/prefix match, while a long token tolerates one or two edits. A cap
// keeps a typo-tolerant search from turning into a noise generator.
func fuzzyDistanceFor(token string) int {
	n := utf8.RuneCountInString(token)
	switch {
	case n < 3:
		return 0
	case n < 6:
		return 1
	default:
		return 2
	}
}

// editDistanceWithin computes the Levenshtein distance between a and b, giving
// up as soon as the distance to the whole candidate already exceeds max plus
// the query runes left to process. It returns ok=false when the distance is
// above max. Callers cap candidate length to keep the DP bounded.
func editDistanceWithin(a, b string, max int) (int, bool) {
	if max < 0 {
		return 0, false
	}
	if a == b {
		return 0, true
	}
	if max == 0 {
		return 0, false
	}
	ar, br := []rune(a), []rune(b)
	if absInt(len(ar)-len(br)) > max {
		return 0, false
	}
	if len(ar) == 0 {
		return len(br), len(br) <= max
	}
	if len(br) == 0 {
		return len(ar), len(ar) <= max
	}

	// Fuzzy candidates are at most 64 bytes, so their rune count fits the
	// stack buffers. Keeping them on the stack removes two allocations per
	// candidate comparison in the fuzzy hot loop.
	n := len(br)
	var prevBuf, curBuf [maxFuzzyCandidateBytes + 1]int
	prev, cur := prevBuf[:n+1], curBuf[:n+1]
	if n > maxFuzzyCandidateBytes {
		prev, cur = make([]int, n+1), make([]int, n+1)
	}
	for j := 0; j <= n; j++ {
		prev[j] = j
	}

	remaining := len(ar)
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		remaining--
		// cur[n] is the distance from a[:i] to the full candidate. Each
		// remaining query rune can lower it by at most one, so a value above
		// max+remaining can never finish inside max.
		if cur[n] > max+remaining {
			return 0, false
		}
		prev, cur = cur, prev
	}
	d := prev[n]
	return d, d <= max
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
