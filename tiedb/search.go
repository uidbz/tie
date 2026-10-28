package tiedb

// Free-text search over the forward-association index.
//
// The store has no substring index: association lookups are exact-value only.
// Rather than materialise tokens or n-grams as triples (which would multiply
// the on-disk records and the reverse-index memory), search derives a
// transient corpus from the in-memory index on first use:
//
//   - candidates are narrowed with the reverse tie-type index when the caller
//     supplies tie-types (tie-type is reverse-indexed by default), so a search
//     over audio files does not walk the whole collection;
//   - every candidate's forward set is read from the association index, and
//     values are reconstructed with levelOf + getValueString from the entry
//     trie. No record-file reads, no blob cache, no writer-queue traffic.
//
// The corpus is cached per (relations, tie-types) pair and invalidated by
// searchGen, which user writes (addLocked/deleteLocked) and a repairing
// CheckIndex bump. Memory is bounded by searchMaxBytes; a corpus that does not
// fit is used for one request and not installed. Cache-off is a supported mode:
// the same code path builds and discards the corpus on every search.

import (
	"cmp"
	"container/heap"
	"slices"
	"strconv"
	"strings"
)

const (
	// DefaultSearchLimit is the page size used when a request omits Limit.
	DefaultSearchLimit = 100
	// MaxSearchLimit is the largest page a request may ask for. Search is an
	// interactive primitive; a caller that wants everything should page.
	MaxSearchLimit = 1000
	// maxSearchOffset keeps a pathological offset from turning the top-K heap
	// into an unbounded allocation.
	maxSearchOffset = 100000
	// maxSearchTokens caps how many query tokens are evaluated.
	maxSearchTokens = 16
	// maxSearchTextRunes caps the query length.
	maxSearchTextRunes = 256
	// maxSearchFieldBytes skips pathologically long field values (paths, blobs
	// stored as strings) so one relation cannot dominate the corpus.
	maxSearchFieldBytes = 1024
	// maxSearchNameBytes bounds one relation, tie-type or weight key. Names are
	// predicates, not data: a caller cannot use a megabyte-long "relation" to
	// inflate a corpus key or a comparison.
	maxSearchNameBytes = 256
	// maxSearchRelations and maxSearchTypes bound the corpus key and the work
	// one request can ask for.
	maxSearchRelations = 16
	maxSearchTypes     = 32
	// maxSearchCorpora bounds the number of cached corpora. The byte budget
	// alone is not enough: a corpus for an unknown relation is empty, so its
	// estimated size is zero while its key and map slot still cost memory.
	maxSearchCorpora = 16

	// defaultSearchCacheBytes is the per-collection corpus budget. Values are
	// shared by text across fields, so this covers a six-figure media library
	// comfortably; a larger library still works with a smaller cache or none.
	defaultSearchCacheBytes = 64 << 20
)

// SearchMode selects the matching strategy. The zero value is SearchWords.
type SearchMode string

const (
	// SearchWords splits the query into tokens and requires every token to
	// match somewhere on the subject (different fields allowed). This is the
	// default and the one a search-as-you-type UI wants.
	SearchWords SearchMode = "words"
	// SearchSubstring requires the whole query to appear in one field, in
	// order. It is the precise phrase mode.
	SearchSubstring SearchMode = "substring"
	// SearchPrefix requires query tokens to match the beginning of a token or
	// field. It never matches an infix or a typo.
	SearchPrefix SearchMode = "prefix"
	// SearchFuzzy is SearchWords plus bounded edit distance for tokens that no
	// exact/prefix/substring match could satisfy.
	SearchFuzzy SearchMode = "fuzzy"
)

// SearchQuery is one free-text request. Relations names the searchable
// predicates; TieTypes optionally restricts the candidate subjects by
// tie-type. An empty Relations returns no matches (the engine does not guess a
// vocabulary for arbitrary callers).
type SearchQuery struct {
	Text      string
	Relations []string
	TieTypes  []string
	Mode      SearchMode
	Offset    int
	Limit     int
	// Weights optionally scales a relation's contribution, in percent
	// (100 = neutral, 200 = twice as important). Unknown relations stay 100.
	Weights map[string]int
}

// SearchMatch is one subject's best match. Relation and Value identify the
// field that contributed most to Score. Attributes is filled by the API layer
// when the caller asks for an expanded result; the engine leaves it nil.
type SearchMatch struct {
	Key        string              `json:"key"`
	Relation   string              `json:"relation"`
	Value      string              `json:"value"`
	Score      int                 `json:"score"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// searchCorpus is an immutable snapshot of the searchable fields of a set of
// subjects. It is shared by concurrent readers without locks.
type searchCorpus struct {
	revision uint64
	subjects []*searchSubject
	bytes    int64
	lastUse  uint64 // guarded by Collection.searchMu; LRU order
}

type searchSubject struct {
	key    string
	fields []searchField
}

type searchField struct {
	relation string
	value    *searchValue
}

// searchValue is a value string shared by every field that carries it, so an
// artist or album name repeated across a thousand tracks is stored once.
type searchValue struct {
	text   string
	norm   string
	tokens []string
}

// Search answers one free-text query, returning the ranked page, the number of
// matching subjects before pagination, and the index revision the page came
// from. An empty query or an empty relation list is a successful empty result.
func (ic *Collection) Search(q SearchQuery) (matches []SearchMatch, total int, revision uint64) {
	q = normalizeSearchQuery(q)
	prepared := prepareSearchQuery(q)
	if prepared.empty() || len(q.Relations) == 0 {
		return nil, 0, ic.searchGen.Load()
	}
	corpus := ic.corpusFor(corpusKey(q), q)
	return rankCorpus(corpus, &prepared, q.Offset, q.Limit)
}

// normalizeSearchQuery clamps every user-controlled dimension and canonicalises
// the relation/type lists (deduplicated and sorted; order is not semantic, and a
// stable order keeps one corpus per selection regardless of how a caller spells
// it).
func normalizeSearchQuery(q SearchQuery) SearchQuery {
	q.Text = truncateSearchText(strings.TrimSpace(q.Text), maxSearchTextRunes)
	switch q.Mode {
	case SearchSubstring, SearchWords, SearchPrefix, SearchFuzzy:
	default:
		q.Mode = SearchWords
	}
	q.Relations = dedupeNonEmpty(q.Relations, maxSearchRelations)
	q.TieTypes = dedupeNonEmpty(q.TieTypes, maxSearchTypes)
	slices.Sort(q.Relations)
	slices.Sort(q.TieTypes)
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Offset > maxSearchOffset {
		q.Offset = maxSearchOffset
	}
	if q.Limit <= 0 {
		q.Limit = DefaultSearchLimit
	}
	if q.Limit > MaxSearchLimit {
		q.Limit = MaxSearchLimit
	}
	if len(q.Weights) > 0 {
		// Weights are only meaningful for relations this request searches, so
		// keying by the capped relation list also bounds the map: at most 16
		// entries, no matter how many a request carried.
		wanted := make(map[string]bool, len(q.Relations))
		for _, relation := range q.Relations {
			wanted[relation] = true
		}
		weights := make(map[string]int, len(q.Relations))
		for relation, w := range q.Weights {
			relation = strings.TrimSpace(relation)
			if !wanted[relation] || w <= 0 {
				continue
			}
			if w < 10 {
				w = 10
			}
			if w > 1000 {
				w = 1000
			}
			weights[relation] = w
		}
		q.Weights = weights
	}
	return q
}

// truncateSearchText cuts s to maxRunes without converting the whole string to
// a rune slice. The walk stops at the byte index of the first rune that would
// exceed the limit, which is also the only allocation-free way to honour the
// limit on a hostile query.
func truncateSearchText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}

func dedupeNonEmpty(values []string, limit int) []string {
	if len(values) == 0 || limit <= 0 {
		return nil
	}
	capacity := len(values)
	if capacity > limit {
		capacity = limit
	}
	out := make([]string, 0, capacity)
	seen := make(map[string]bool, capacity)
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > maxSearchNameBytes || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) == limit {
			break
		}
	}
	return out
}

// corpusKey identifies the field/type selection of a corpus. Mode, weights and
// paging are query-time concerns and deliberately not part of the key. Every
// name is length-prefixed and each list carries its own count, so names that
// contain separators (or control bytes) cannot make two selections collide.
// Relations and types are sorted by normalizeSearchQuery, so callers that list
// the same selection in a different order share one corpus.
func corpusKey(q SearchQuery) string {
	var b strings.Builder
	b.Grow(32 + len(q.Relations)*8 + len(q.TieTypes)*8)
	writeList := func(values []string) {
		b.WriteString(strconv.Itoa(len(values)))
		b.WriteByte(';')
		for _, v := range values {
			b.WriteString(strconv.Itoa(len(v)))
			b.WriteByte(':')
			b.WriteString(v)
		}
	}
	writeList(q.Relations)
	b.WriteByte('|')
	writeList(q.TieTypes)
	return b.String()
}

// corpusFor returns the cached corpus for key, building it when absent or when
// the index has changed. searchMu guards the cache map only: a cold build runs
// outside it, so a warm search for another selection is not parked behind a
// large build. Two workers that race on the same key may both build, but only
// one corpus is installed; the other reuses it.
func (ic *Collection) corpusFor(key string, q SearchQuery) *searchCorpus {
	ic.searchMu.Lock()
	revision := ic.searchGen.Load()
	ic.dropStaleCorporaLocked(revision)
	if c, ok := ic.searchCache[key]; ok && c.revision == revision {
		ic.searchUse++
		c.lastUse = ic.searchUse
		ic.searchMu.Unlock()
		return c
	}
	ic.searchMu.Unlock()

	c := ic.buildCorpus(q, revision)

	ic.searchMu.Lock()
	defer ic.searchMu.Unlock()
	if ic.searchGen.Load() != revision || ic.searchMaxBytes <= 0 {
		return c
	}
	if existing, ok := ic.searchCache[key]; ok && existing.revision == revision {
		// Another search built and installed this selection while we were
		// building. Reuse its corpus instead of replacing it with an equal one.
		ic.searchUse++
		existing.lastUse = ic.searchUse
		return existing
	}
	ic.installCorpusLocked(key, c)
	return c
}

// dropStaleCorporaLocked discards every corpus whose revision is older than the
// current generation. Without this, a write leaves every cached selection in
// memory until its own key is searched again. Caller holds searchMu.
func (ic *Collection) dropStaleCorporaLocked(revision uint64) {
	for key, c := range ic.searchCache {
		if c.revision == revision {
			continue
		}
		ic.searchBytes -= c.bytes + int64(len(key))
		delete(ic.searchCache, key)
	}
}

// installCorpusLocked stores c and evicts least-recently-used corpora until the
// collection is under its byte and entry budgets. Key bytes count toward the
// budget too: an empty corpus still costs a map entry and its selection string.
// Caller holds searchMu.
func (ic *Collection) installCorpusLocked(key string, c *searchCorpus) {
	if ic.searchCache == nil {
		ic.searchCache = make(map[string]*searchCorpus)
	}
	if old, ok := ic.searchCache[key]; ok {
		ic.searchBytes -= old.bytes + int64(len(key))
	}
	ic.searchUse++
	c.lastUse = ic.searchUse
	ic.searchCache[key] = c
	ic.searchBytes += c.bytes + int64(len(key))

	for (ic.searchBytes > ic.searchMaxBytes || len(ic.searchCache) > maxSearchCorpora) && len(ic.searchCache) > 0 {
		var eldest string
		var eldestUse uint64
		first := true
		for k, v := range ic.searchCache {
			if first || v.lastUse < eldestUse {
				eldest, eldestUse, first = k, v.lastUse, false
			}
		}
		ic.searchBytes -= ic.searchCache[eldest].bytes + int64(len(eldest))
		delete(ic.searchCache, eldest)
		if eldest == key {
			// The corpus just built is itself over budget: use it for this
			// request, but do not keep it (or evict everything else trying).
			break
		}
	}
}

// buildCorpus walks the in-memory index for the requested relations and types.
// It never reads the record file: entry ids are resolved against the trie and
// the association sets, both of which are resident.
func (ic *Collection) buildCorpus(q SearchQuery, revision uint64) *searchCorpus {
	corpus := &searchCorpus{revision: revision}

	// Relation name -> entry id, inverted for the forward-set walk.
	relIDs := make(map[uint64]string, len(q.Relations))
	for _, relation := range q.Relations {
		if id, _, ok := ic.getEntryFromString(relation); ok {
			relIDs[id] = relation
		}
	}
	if len(relIDs) == 0 {
		return corpus
	}

	shared := make(map[string]*searchValue)
	// Entry text is immutable for the life of a collection, and ids are
	// collection-global, so one build can reuse a value's reconstructed text
	// instead of walking the trie again for every track that carries it.
	textByID := make(map[uint64]string)
	textFor := func(level int, id uint64) string {
		if text, ok := textByID[id]; ok {
			return text
		}
		text := ic.getValueString(level, id)
		textByID[id] = text
		return text
	}
	valueFor := func(text string) *searchValue {
		if v, ok := shared[text]; ok {
			return v
		}
		norm := foldSearchText(text)
		v := &searchValue{text: text, norm: norm, tokens: searchTokens(norm)}
		shared[text] = v
		return v
	}

	addSubject := func(subjectID uint64, level int, set *AssociationSet) {
		// Snapshot the requested association entries first: AssociationSet.ForEach
		// may hold a shard read lock across the callback, and resolving a value
		// takes entry-tree locks. Doing the resolution after ForEach returns
		// keeps the lock layers in the order doc.go documents. Filtering inside
		// the callback is only a map lookup; it keeps unrelated metadata out of
		// the snapshot.
		refs := make([]UniqueAssociation, 0, 8)
		set.ForEach(func(ua UniqueAssociation, _ int64) {
			if _, ok := relIDs[ua.Relation]; ok {
				refs = append(refs, ua)
			}
		})
		if len(refs) == 0 {
			return
		}
		key := textFor(level, subjectID)
		if key == "" {
			return
		}
		fields := make([]searchField, 0, len(refs))
		for _, ua := range refs {
			relation := relIDs[ua.Relation]
			valueLevel, ok := ic.levelOf(ua.AssociateTo)
			if !ok {
				continue
			}
			text := textFor(valueLevel, ua.AssociateTo)
			if text == "" || len(text) > maxSearchFieldBytes {
				continue
			}
			fields = append(fields, searchField{relation: relation, value: valueFor(text)})
		}
		if len(fields) == 0 {
			return
		}
		corpus.subjects = append(corpus.subjects, &searchSubject{key: key, fields: fields})
		corpus.bytes += int64(len(key)) + 48
		for _, f := range fields {
			corpus.bytes += int64(len(f.value.text)+len(f.value.norm)+len(f.value.tokens)*16) + 32
		}
	}

	// Narrowed path: the reverse tie-type index names the candidate subjects
	// directly, so a search over one media type never visits the rest of the
	// collection. A server that removed tie-type from ReverseRelations falls
	// through to the full scan below, which still filters by type.
	if len(q.TieTypes) > 0 && ic.reverseIndexed("tie-type") {
		tieTypeID, _, ok := ic.getEntryFromString("tie-type")
		if !ok {
			return corpus
		}
		seen := make(map[uint64]bool)
		for _, tieType := range q.TieTypes {
			reversed, found := ic.GetReverseAssociations(tieType)
			if !found {
				continue
			}
			// Collect ids under the reverse set lock, resolve afterwards.
			var subjects []uint64
			reversed.ForEach(func(ua UniqueAssociation, _ int64) {
				if ua.Relation != tieTypeID || seen[ua.AssociateTo] {
					return
				}
				seen[ua.AssociateTo] = true
				subjects = append(subjects, ua.AssociateTo)
			})
			for _, subjectID := range subjects {
				level, ok := ic.levelOf(subjectID)
				if !ok {
					continue
				}
				addSubject(subjectID, level, ic.getAssociations(level, subjectID))
			}
		}
		return corpus
	}

	// Full scan. When tie-types were requested but the reverse index cannot
	// answer them, filter by direct membership in the subject's forward set —
	// one point lookup per type, no value resolution.
	var typeIDs map[uint64]bool
	var tieTypeID uint64
	if len(q.TieTypes) > 0 {
		id, _, ok := ic.getEntryFromString("tie-type")
		if !ok {
			return corpus
		}
		tieTypeID = id
		typeIDs = make(map[uint64]bool, len(q.TieTypes))
		for _, tieType := range q.TieTypes {
			if id, _, ok := ic.getEntryFromString(tieType); ok {
				typeIDs[id] = true
			}
		}
		if len(typeIDs) == 0 {
			return corpus
		}
	}

	for _, ft := range ic.forwardTrees() {
		level := ft.level
		ft.tree.ForEach(func(subjectID uint64, set *AssociationSet) {
			if typeIDs != nil && !hasAnyType(set, tieTypeID, typeIDs) {
				return
			}
			addSubject(subjectID, level, set)
		})
	}
	return corpus
}

// reverseIndexed reports whether relation is in this collection's reverse
// index. A nil set means every relation is indexed (the engine default).
func (ic *Collection) reverseIndexed(relation string) bool {
	if ic.reverseRelations == nil {
		return true
	}
	return ic.reverseRelations[relation]
}

// hasAnyType reports whether set carries (subject, tie-type, one of typeIDs).
// The forward set is keyed by {value2, value1}, so each type is one lookup.
func hasAnyType(set *AssociationSet, tieTypeID uint64, typeIDs map[uint64]bool) bool {
	for id := range typeIDs {
		if _, ok := set.Get(UniqueAssociation{AssociateTo: id, Relation: tieTypeID}); ok {
			return true
		}
	}
	return false
}

// rankCorpus scores every subject and keeps the best offset+limit hits in a
// min-heap, so a broad query does not allocate or sort every match while still
// reporting an exact total.
func rankCorpus(c *searchCorpus, q *preparedSearchQuery, offset, limit int) ([]SearchMatch, int, uint64) {
	capacity := offset + limit
	hits := make(searchHitHeap, 0, min(capacity, 64))
	total := 0
	for _, subject := range c.subjects {
		ok, score, field := q.match(subject)
		if !ok {
			continue
		}
		total++
		if capacity <= 0 {
			continue
		}
		m := SearchMatch{
			Key:      subject.key,
			Relation: subject.fields[field].relation,
			Value:    subject.fields[field].value.text,
			Score:    score,
		}
		if len(hits) < capacity {
			heap.Push(&hits, m)
			continue
		}
		if compareSearchMatch(m, hits[0]) > 0 {
			heap.Pop(&hits)
			heap.Push(&hits, m)
		}
	}

	page := make([]SearchMatch, len(hits))
	copy(page, hits)
	slices.SortFunc(page, func(a, b SearchMatch) int {
		return -compareSearchMatch(a, b)
	})
	if offset >= len(page) {
		return []SearchMatch{}, total, c.revision
	}
	return page[offset:], total, c.revision
}

// compareSearchMatch orders better matches higher: score desc, relation asc,
// value asc, key asc. The last three make the ordering total and deterministic.
func compareSearchMatch(a, b SearchMatch) int {
	if n := cmp.Compare(a.Score, b.Score); n != 0 {
		return n
	}
	if n := cmp.Compare(b.Relation, a.Relation); n != 0 {
		return n
	}
	if n := cmp.Compare(b.Value, a.Value); n != 0 {
		return n
	}
	return cmp.Compare(b.Key, a.Key)
}

// searchHitHeap is a min-heap whose root is the worst retained hit.
type searchHitHeap []SearchMatch

func (h searchHitHeap) Len() int           { return len(h) }
func (h searchHitHeap) Less(i, j int) bool { return compareSearchMatch(h[i], h[j]) < 0 }
func (h searchHitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *searchHitHeap) Push(x any)        { *h = append(*h, x.(SearchMatch)) }
func (h *searchHitHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
