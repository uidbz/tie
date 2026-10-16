package tiedb

// Ordering: resolving stored triples into strings, sorting them, and paging.

import (
	"cmp"
	"slices"
	"strconv"
)

// relationValues reads the values key holds under relation, forward only and
// restricted to that relation's id (§5.2 item 6). Sort's value-based ordering
// used to call Get per key, which runs a full Sort — resolve, decode, sort,
// build a TripleSet of nested maps — to answer one lookup, and does it once per
// key being ordered. Here the association set is filtered by relation id and
// only the matching records are resolved.
func (ic *Collection) relationValues(key, relation string) ([]string, bool) {
	tree, found := ic.GetAssociations(key)
	if !found {
		return nil, false
	}
	relationID, _, known := ic.getEntryFromString(relation)
	if !known {
		return nil, false
	}
	var out []string
	tree.ForEach(func(ua UniqueAssociation, pos int64) {
		if ua.Relation != relationID {
			return
		}
		t, ok := ic.resolveEntry(ua, pos)
		if !ok || t.Value1 != relationID {
			return
		}
		out = append(out, ic.getValueString(t.Value2Level, t.Value2))
	})
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// firstValueOf returns the smallest value the key holds under relation, or ""
// if it holds none. Used by Sort's value-based ordering (SortByValue); picking
// the smallest keeps the ordering deterministic for multi-valued relations.
func (ic *Collection) firstValueOf(key, relation string) string {
	values, found := ic.relationValues(key, relation)
	if !found {
		return ""
	}
	best := ""
	for i, v := range values {
		if i == 0 || v < best {
			best = v
		}
	}
	return best
}

// firstNumberOf is firstValueOf for numeric ordering: the smallest value the key
// holds under relation that parses as a number, or ok=false if it holds none that
// do. It picks the numeric minimum rather than parsing the lexicographic minimum,
// so a multi-valued relation orders by the same value a reader would call its
// smallest.
func (ic *Collection) firstNumberOf(key, relation string) (float64, bool) {
	values, found := ic.relationValues(key, relation)
	if !found {
		return 0, false
	}
	best := 0.0
	any := false
	for _, v := range values {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if !any || n < best {
			best = n
			any = true
		}
	}
	return best, any
}

// loadTriples walks an association subtree and emits each Triple. It is a pure
// in-memory traversal: memory-only mode reads resident Triples, disk mode
// resolves positions through the cache/disk. No collection-wide serialization.
func (ic *Collection) makeStringTriple(t Triple) StringTriple {
	st := StringTriple{}
	st.Key = ic.getValueString(t.Level, t.Key)
	st.Value1 = ic.getValueString(t.Value1Level, t.Value1)
	st.Value2 = ic.getValueString(t.Value2Level, t.Value2)

	return st
}

type SortOptions struct {
	Offset int
	// Limit caps the number of TRIPLES (not rows). Negative means unlimited.
	// A zero Limit gets the server default of 1000 — clients must send -1 for
	// "no limit" (client.QueryIn maps 0 to -1; see issue #4).
	Limit  int
	SortBy string // Value1 to sort by
	// SortByValue orders matched keys by the VALUE each key holds under this
	// relation (a forward lookup per key), rather than by the matched triple.
	// Empty means no value-based ordering. Example: "gendb-imported-at" to sort
	// tables chronologically. Multi-valued relations sort by their smallest value.
	SortByValue string
	// SortByValueNumeric reads each SortByValue value as a number and orders
	// numerically instead of lexicographically, so 9 precedes 10. Values that do
	// not parse as a number sort last, independent of Descending — the same rule
	// as a missing value, and what keeps the comparator consistent. Ignored when
	// SortByValue is empty. Dates need no such mode: the RFC3339 and YYYY-MM-DD
	// forms already sort chronologically as strings.
	SortByValueNumeric bool
	// Descending reverses the final ordering (applies to any sort mode).
	Descending bool
}

func (ic *Collection) Sort(tree *AssociationSet, value1Filter string, o SortOptions) (sorted []StringTriple, totalCount int) {
	if o.Limit == 0 {
		o.Limit = 1000
	}
	// Resolve the relation filter to its entry id once, and skip the entries
	// that cannot match before resolveEntry touches the record file (§5.2 item
	// 5). The relation id is already in the UniqueAssociation key, so a
	// high-fan-in node read through Get(hub, rel) no longer pays for every
	// other relation it holds. An unresolvable filter means no triple in this
	// collection carries that relation at all.
	var filterID uint64
	filterActive := value1Filter != ""
	if filterActive {
		id, _, found := ic.getEntryFromString(value1Filter)
		if !found {
			return []StringTriple{}, 0
		}
		filterID = id
	}

	// Resolve straight into a slice sized by the set's cardinality. The old
	// shape — a 10 000-slot channel, a goroutine, and a 1000-triple
	// preallocation — cost 533 KB per call regardless of how little it
	// matched: the single largest allocation in the store (a 1000-row Expand
	// churned ~266 MB, a dump of 20k subjects ~10 GB).
	//
	// A relation filter makes the set's cardinality the wrong estimate: a hub
	// with 2 000 relations read through one of them would preallocate 2 000
	// slots to hold a handful, so the filtered path starts small and grows.
	if filterActive {
		sorted = nil
	} else {
		sorted = make([]StringTriple, 0, tree.Size())
	}
	tree.ForEach(func(ua UniqueAssociation, pos int64) {
		// Entry ids are unique per string, so the id test replaces the string
		// comparison that used to happen after decoding.
		if filterActive && ua.Relation != filterID {
			return
		}
		t, ok := ic.resolveEntry(ua, pos)
		if !ok {
			return
		}
		sorted = append(sorted, ic.makeStringTriple(t))
	})

	// When ordering by a relation's value, resolve each key's value once and
	// cache it — the comparator runs O(n log n) times but the forward lookup
	// (and, in numeric mode, the parse) runs at most once per distinct key.
	var valueOf func(key string) string
	var numberOf func(key string) (float64, bool)
	if o.SortByValue != "" && o.SortByValueNumeric {
		type parsed struct {
			n  float64
			ok bool
		}
		cache := make(map[string]parsed, len(sorted))
		numberOf = func(key string) (float64, bool) {
			if p, ok := cache[key]; ok {
				return p.n, p.ok
			}
			n, found := ic.firstNumberOf(key, o.SortByValue)
			cache[key] = parsed{n, found}
			return n, found
		}
	} else if o.SortByValue != "" {
		cache := make(map[string]string, len(sorted))
		valueOf = func(key string) string {
			if v, ok := cache[key]; ok {
				return v
			}
			v := ic.firstValueOf(key, o.SortByValue)
			cache[key] = v
			return v
		}
	}

	slices.SortFunc(sorted, func(a, b StringTriple) int {
		var n int
		if numberOf != nil {
			na, oka := numberOf(a.Key)
			nb, okb := numberOf(b.Key)
			// Unparseable values sort last in both directions, so return before
			// the Descending negation below. A comparator that let both
			// Less(a,b) and Less(b,a) hold would corrupt the slice.
			if !oka || !okb {
				if oka == okb {
					return cmp.Compare(a.Key, b.Key)
				}
				if oka {
					return -1
				}
				return 1
			}
			if n = cmp.Compare(na, nb); n == 0 {
				n = cmp.Compare(a.Key, b.Key)
			}
		} else if o.SortByValue != "" {
			if n = cmp.Compare(valueOf(a.Key), valueOf(b.Key)); n == 0 {
				n = cmp.Compare(a.Key, b.Key)
			}
		} else if o.SortBy == "" {
			if n = cmp.Compare(a.Key, b.Key); n == 0 {
				if n = cmp.Compare(a.Value1, b.Value1); n == 0 {
					n = cmp.Compare(a.Value2, b.Value2)
				}
			}
		} else {
			if a.Value1 != o.SortBy {
				n = 1
			} else if b.Value1 != o.SortBy {
				n = -1
			} else if n = cmp.Compare(a.Key, b.Key); n == 0 {
				n = cmp.Compare(a.Value2, b.Value2)
			}
		}
		if o.Descending {
			return -n
		}
		return n
	})

	count := len(sorted)

	if o.Offset < 0 {
		o.Offset = 0
	}

	if count < o.Offset {
		return make([]StringTriple, 0), count
	}

	if o.Limit < 0 || count < o.Offset+o.Limit {
		return sorted[o.Offset:], count
	}

	return sorted[o.Offset : o.Offset+o.Limit], count
}

func (ic *Collection) GetTripleSet(s *AssociationSet, value1Filter string, o SortOptions) (result TripleSet, totalCount int) {
	result, _, totalCount = ic.GetPage(s, value1Filter, o)
	return result, totalCount
}

// GetPage runs one Sort and returns both views of the page: the unordered
// TripleSet map (for the existing OneKey/OneValue2 helpers and back-compat) and
// the ordered, paginated slice (for clients that need a stable sequence),
// together with the pre-pagination total. Building both from a single Sort
// avoids sorting twice.
func (ic *Collection) GetPage(s *AssociationSet, value1Filter string, o SortOptions) (result TripleSet, sorted []StringTriple, totalCount int) {
	result = make(TripleSet)

	sorted, totalCount = ic.Sort(s, value1Filter, o)

	for _, t := range sorted {
		if !result.Has(t.Key) {
			result[t.Key] = make(Value1)
		}
		if !result[t.Key].Has(t.Value1) {
			result[t.Key][t.Value1] = make(Value2)
		}
		if !result[t.Key][t.Value1].Has(t.Value2) {
			result[t.Key][t.Value1][t.Value2] = Unit{}
		}
	}

	return result, sorted, totalCount
}
