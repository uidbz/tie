package tiedb

// Query paths: TagQuery/QueryTags, ExpandKeys and the plain Get lookup.

func (ic *Collection) Get(key string, value1 string) (TripleSet, bool) {
	if tree, found := ic.GetAssociations(key); found {
		data, _ := ic.GetTripleSet(tree, value1, SortOptions{Limit: -1})
		if data != nil {
			if reverse, found := ic.GetReverseAssociations(key); found {
				associated, _ := ic.GetTripleSet(reverse, value1, SortOptions{Limit: -1})
				associated.ForEachKey(func(key string) {
					data[key] = associated[key]
				})
			}
			return data, true
		} else {
			return nil, false
		}
	} else {
		return nil, false
	}
}

// TagQuery selects entries by association membership: a match must be associated
// with the seed (Include[0]) and with every other Include value, and with none of
// the Exclude values. It expresses "things tagged with all of these but none of
// those" directly, so callers no longer compose raw set operations. Include and
// Exclude terms are matched by reverse association; Reverse also selects the seed
// via reverse associations (the tag-query case) rather than forward.
//
// Scope, when set, restricts matches to the associates of that value under a
// different relation than the Include/Exclude terms — e.g. scope a "tag" query to
// a "tie-type" so only audio files are returned. Because Scope's relation differs
// from the query terms' relation, it cannot be an Include term (intersect keys on
// the full {associate, relation} pair); it is applied by matching on associate
// identity alone. An empty Scope means no scoping.
type TagQuery struct {
	Include         []string    // AND across all; Include[0] is the seed set
	Exclude         []string    // NOT any of these
	Scope           string      // restrict to associates of this value, ignoring relation
	MissingRelation string      // keep only matches with NO triple under this relation
	Reverse         bool        // seed via reverse associations (tag query) vs forward
	Filter          string      // filter-in on value1 (relation)
	Sort            SortOptions // pagination (Offset/Limit/SortBy)
}

// QueryTags resolves the include/exclude set algebra internally and returns a
// paginated result: the unordered TripleSet, the ordered+paged slice, and the
// pre-pagination total, plus whether the seed existed. The intermediate
// association trees never leave tiedb.
func (ic *Collection) QueryTags(q TagQuery) (result TripleSet, sorted []StringTriple, total int, found bool) {
	if len(q.Include) == 0 {
		return make(TripleSet), nil, 0, false
	}

	var set *AssociationSet
	if q.Reverse {
		set, found = ic.GetReverseAssociations(q.Include[0])
	} else {
		set, found = ic.GetAssociations(q.Include[0])
	}
	if !found {
		return make(TripleSet), nil, 0, false
	}

	for _, tag := range q.Include[1:] {
		other, ok := ic.GetReverseAssociations(tag)
		if !ok {
			return make(TripleSet), nil, 0, true // an unmet AND term yields no matches
		}
		set = set.intersect(other)
	}

	for _, tag := range q.Exclude {
		if other, ok := ic.GetReverseAssociations(tag); ok {
			set = set.exclude(other)
		}
	}

	if q.Scope != "" {
		scope, ok := ic.GetReverseAssociations(q.Scope)
		if !ok {
			return make(TripleSet), nil, 0, true // nothing carries the scope value
		}
		set = set.intersectByAssociate(scope)
	}

	if q.MissingRelation != "" {
		set = ic.filterMissingRelation(set, q.MissingRelation)
	}

	result, sorted, total = ic.GetPage(set, q.Filter, q.Sort)
	return result, sorted, total, true
}

// filterMissingRelation returns the subset of seed whose subject carries no
// forward triple under relation. It expresses "of these matches, keep the ones
// that lack a tag" without a full-collection scan or client-side download: each
// seed entry's subject is looked up in the forward index (a tiny per-subject
// set) and dropped if it has any association under the relation.
//
// This is the negation-of-existence the association algebra cannot express with
// Exclude (which removes a specific value, not the presence of a relation). Cost
// is O(len(seed)) forward point-lookups; memory is O(result), so it respects the
// engine's memory-over-speed weighting even on a metadata-heavy store.
func (ic *Collection) filterMissingRelation(seed *AssociationSet, relation string) *AssociationSet {
	relID, _, found := ic.getEntryFromString(relation)
	out := newAssociationSet()
	seed.ForEach(func(key UniqueAssociation, pos int64) {
		if !found {
			// The relation was never interned, so nothing carries it: every
			// seed entry qualifies as "missing" it.
			out.Put(key, pos)
			return
		}
		// The reverse-set entry carries the subject id but not its level; the
		// stored triple does, so resolve it to reach the subject's forward set.
		t, ok := ic.resolveEntry(key, pos)
		if !ok {
			return
		}
		if !ic.getAssociations(t.Level, t.Key).HasRelation(relID) {
			out.Put(key, pos)
		}
	})
	return out
}

// ExpandKeys returns one Row per key holding that key's forward attributes
// (relation -> values), reusing the same GetAssociations + Sort path a query
// uses. Keys with no associations are skipped. Order follows the input keys.
// This is the multi-key batch fetch that lets a client attach metadata to many
// matches (or list many entries) in one round trip instead of one Get per key.
func (ic *Collection) ExpandKeys(keys []string, filter string) []Row {
	rows := make([]Row, 0, len(keys))
	for _, key := range keys {
		tree, found := ic.GetAssociations(key)
		if !found {
			continue
		}
		sorted, _ := ic.Sort(tree, filter, SortOptions{Limit: -1})
		if len(sorted) == 0 && filter == "" {
			// The set outlives its last triple (outer keys are never removed),
			// so a fully-deleted subject would otherwise expand to an empty
			// row and look like it still exists.
			continue
		}
		attrs := make(map[string][]string)
		for _, t := range sorted {
			attrs[t.Value1] = append(attrs[t.Value1], t.Value2)
		}
		rows = append(rows, Row{Key: key, Attributes: attrs})
	}
	return rows
}
