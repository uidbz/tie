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

	// No triple in the collection carries that relation: nothing to report,
	// without walking the matches at all.
	tagRelID, _, known := ic.getEntryFromString(tagRelation)
	if !known {
		return nil, true
	}

	// The answer is tag values, never the keys — so the keys are never decoded.
	// One pass over the matched set resolves each association once, and only to
	// learn the subject it names; association entries sharing a subject are
	// skipped before the record read (§5.2 item 8).
	type subject struct {
		level int
		id    uint64
	}
	var subjects []subject
	seenSubject := make(map[uint64]bool, set.Size())
	seenAssoc := make(map[uint64]bool, set.Size())
	set.ForEach(func(ua UniqueAssociation, pos int64) {
		// Within one set the same AssociateTo always names the same subject:
		// in a reverse set it is the subject id, in a forward set every entry
		// shares the seed.
		if seenAssoc[ua.AssociateTo] {
			return
		}
		seenAssoc[ua.AssociateTo] = true
		t, ok := ic.resolveEntry(ua, pos)
		if !ok {
			return
		}
		if seenSubject[t.Key] {
			return
		}
		seenSubject[t.Key] = true
		subjects = append(subjects, subject{level: t.Level, id: t.Key})
	})

	// Second pass: each subject's values for tagRelation, filtered by relation
	// id before the record read, and deduplicated by value id — a tag carried
	// by a thousand files is decoded once, not a thousand times. The forward
	// set is reached by (level, id), so the keys are never re-interned.
	seenTag := make(map[uint64]bool)
	var tags []string
	for _, s := range subjects {
		tree := ic.getAssociations(s.level, s.id)
		if tree.Size() == 0 {
			continue
		}
		tree.ForEach(func(ua UniqueAssociation, pos int64) {
			if ua.Relation != tagRelID {
				return
			}
			t, ok := ic.resolveEntry(ua, pos)
			if !ok || t.Value1 != tagRelID {
				return
			}
			if seenTag[t.Value2] {
				return
			}
			seenTag[t.Value2] = true
			tags = append(tags, ic.getValueString(t.Value2Level, t.Value2))
		})
	}

	return tags, true
}
