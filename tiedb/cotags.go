package tiedb

// CoTags runs the same set algebra as QueryTags (same Include/Exclude/Scope/Reverse)
// and returns every unique tagRelation value2 carried by the matching entries.
// This is a "faceted refinement" query: given the current tag filter, what further
// tags exist on the matching set? Use tagRelation = "tag" for the standard case.
//
// Returns (nil, false) when the seed term (Include[0]) has no reverse associations.
// Returns (nil, true) when the AND of include terms yields an empty set (unmet AND).
// The returned slice is unsorted and deduplicated; sort it in the caller if needed.
func (ic *Collection) CoTags(q TagQuery, tagRelation string) ([]string, bool) {
	if len(q.Include) == 0 {
		return nil, false
	}

	set, found, empty := ic.resolveQuery(q)
	if !found {
		return nil, false
	}
	if empty {
		return nil, true
	}

	// Resolve the result set to unique key strings (the matching content hashes).
	// Sort with no value1 filter and Limit=-1 to enumerate all triples; the Key
	// field of each triple is the content hash we care about.
	allTriples, _ := ic.Sort(set, "", SortOptions{Limit: -1})
	seenKey := make(map[string]bool, len(allTriples))
	keys := make([]string, 0, len(allTriples))
	for _, t := range allTriples {
		if !seenKey[t.Key] {
			seenKey[t.Key] = true
			keys = append(keys, t.Key)
		}
	}

	// For each matching hash, collect all of its tagRelation values via its
	// forward associations. This is the same path ExpandKeys uses for Expand
	// queries, restricted to one relation.
	seenTag := make(map[string]bool)
	var tags []string
	for _, key := range keys {
		tree, ok := ic.GetAssociations(key)
		if !ok {
			continue
		}
		tagTriples, _ := ic.Sort(tree, tagRelation, SortOptions{Limit: -1})
		for _, t := range tagTriples {
			if !seenTag[t.Value2] {
				seenTag[t.Value2] = true
				tags = append(tags, t.Value2)
			}
		}
	}

	return tags, true
}
