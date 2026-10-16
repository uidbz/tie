package tiedb

// Forward and reverse association maintenance: the write paths (Add, Delete,
// Update, SetValues) and the association lookups they share.

import (
	"log/slog"
	"sync/atomic"
)

// shouldBuildReverse reports whether a triple's relation (value1) should get a
// reverse-association node. A nil reverseRelations set means index everything.
func (ic *Collection) shouldBuildReverse(a *Triple) bool {
	if ic.reverseRelations == nil {
		return true
	}
	return ic.reverseRelations[ic.getValueString(a.Value1Level, a.Value1)]
}

func (ic *Collection) Add(key string, value1 string, value2 string) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	ic.addLocked(key, value1, value2)
}

// addLocked is Add's body; the caller must hold changeMutex. The *Locked
// helpers let composite operations (Update, SetValues, batches) run several
// mutations as one critical section: a concurrent reader never sees the
// relation momentarily empty, and two concurrent Sets cannot interleave into
// the union of both value sets.
func (ic *Collection) addLocked(key string, value1 string, value2 string) {
	keyID, keyLevel := ic.insertLocked(key)
	value1ID, value1Level := ic.insertLocked(value1)
	value2ID, value2Level := ic.insertLocked(value2)

	ass := Triple{
		Key:         keyID,
		Level:       keyLevel,
		Value1Level: value1Level,
		Value1:      value1ID,
		Value2Level: value2Level,
		Value2:      value2ID,
	}

	// The exists-check, slot reservation and index insert run as one critical
	// section: two concurrent Adds of the same triple used to both pass the
	// check, each write a full record to its own slot, and each overwrite the
	// forward and reverse entries independently — leaving one live record on
	// disk that no index owned, which a later Delete could not tombstone and
	// that resurrected the triple at the next load. changeMutex is the same
	// lock Delete holds, so an Add can no longer interleave with a Delete of the
	// same triple between the check and the insert either.
	if ic.uniqueAssociationExists(keyLevel, keyID, value1ID, value2ID) {
		return
	}

	// Reserve the final storage position now and insert at it, so a duplicate
	// Add queued before the writer runs is deduplicated and a concurrent Delete
	// sees the real position (never a placeholder that the writer would later
	// overwrite, which used to resurrect deleted triples). In disk mode the
	// writer only persists the bytes at this offset; in memory mode the position
	// is the arena slot.
	var pos int64
	if ic.writeToDisk {
		pos = ic.allocSlot()
		// Enqueue the write BEFORE making the triple visible in the index.
		// Both steps run under changeMutex, and a channel send happens-before
		// the matching receive, so any reader that can see the index entry
		// finds the FILE_ADD already queued and drainWriteQueue applies it.
		// The old order (index, then enqueue) let a RECLAIMED slot be read as
		// its old tombstone: ReadAt succeeded with stale bytes, decoding
		// failed, and the triple silently vanished from that query.
		ic.finishedAdding.Add(1)
		ic.dBWriteQueue <- FileMod{
			EntryType: TYPE_ASSOCIATION,
			Level:     keyLevel,
			Mode:      FILE_ADD,
			Position:  pos,
			Triple:    &ass,
		}
	} else {
		pos = ic.arenaStore(&ass)
	}
	ic.insertAssociation(keyLevel, &ass, pos)
}

func (ic *Collection) GetAssociations(value string) (*AssociationSet, bool) {
	if entryID, level, found := ic.getEntryFromString(value); !found {
		return newAssociationSet(), false
	} else {
		return ic.getAssociations(level, entryID), true
	}
}

func (ic *Collection) GetReverseAssociations(value string) (*AssociationSet, bool) {
	if entryID, level, found := ic.getEntryFromString(value); !found {
		return newAssociationSet(), false
	} else {
		return ic.getReverseAssociations(level, entryID), true
	}
}

// Delete removes (key, value1, value2) from both indexes and tombstones its
// record. The forward index is authoritative; the reverse entry is removed by
// key, whatever position it holds.
//
// When the forward entry is absent but a reverse entry for the same triple
// exists — a phantom: the forward insert was lost, or the forward entry was
// already deleted while the reverse one lingered — that residue is removed and
// the call succeeds, so a phantom that reverse queries keep returning can be
// cleared with an ordinary delete instead of a server restart.
func (ic *Collection) Delete(key string, value1 string, value2 string) (string, bool) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	return ic.deleteLocked(key, value1, value2)
}

// deleteLocked is Delete's body; the caller must hold changeMutex.
func (ic *Collection) deleteLocked(key string, value1 string, value2 string) (string, bool) {
	k, _, fk := ic.getEntryFromString(key)
	v1, _, f1 := ic.getEntryFromString(value1)
	v2, l2, f2 := ic.getEntryFromString(value2)
	if !fk {
		return "Did not find '" + key + "'", false
	}
	if !f1 || !f2 {
		return "Did not find '" + value2 + "' with value1 '" + value1 + "'", false
	}

	assKey := UniqueAssociation{AssociateTo: v2, Relation: v1}
	reverseAssKey := UniqueAssociation{AssociateTo: k, Relation: v1}
	rev := ic.getReverseAssociations(l2, v2)

	if asses, found := ic.GetAssociations(key); found {
		if pos, found := asses.Get(assKey); found {
			ic.deleteAssociation(asses, assKey, pos)
			rev.Delete(reverseAssKey)
			return "", true
		}
	}
	if _, found := rev.Get(reverseAssKey); found {
		rev.Delete(reverseAssKey)
		return "removed stale reverse-index entry for '" + key + "' " + value1 + " '" + value2 + "'", true
	}
	if _, found := ic.GetAssociations(key); !found {
		return "Did not find '" + key + "'", false
	}
	return "Did not find '" + value2 + "' with value1 '" + value1 + "'", false
}

// Update replaces value2 with newValue2 under a single changeMutex hold: a
// concurrent reader never observes the relation momentarily empty, and the
// delete+add cannot interleave with another writer's pair.
func (ic *Collection) Update(key string, value1 string, value2 string, newValue2 string) (string, bool) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	if msg, ok := ic.deleteLocked(key, value1, value2); ok {
		ic.addLocked(key, value1, newValue2)
		return "", true
	} else {
		return msg, false
	}
}

// UpdateAdd replaces value2 with newValue2, adding newValue2 even when the
// original (key, value1, value2) did not exist. It always succeeds; the message
// notes when a fallback add was used instead of an update.
func (ic *Collection) UpdateAdd(key string, value1 string, value2 string, newValue2 string) (string, bool) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	if msg, ok := ic.deleteLocked(key, value1, value2); ok {
		ic.addLocked(key, value1, newValue2)
		return "", true
	} else {
		ic.addLocked(key, value1, newValue2)
		return msg + ": could not update; adding new value as requested.", true
	}
}

// SimpleUpdate makes a scalar (single-valued) field equal to newValue2:
// it removes every existing value2 for (key, value1) and adds newValue2.
// When the field does not yet exist it adds newValue2 only if addOnFail is set.
// Use this for fields that are meant to hold exactly one value.
func (ic *Collection) SimpleUpdate(key string, value1 string, newValue2 string, addOnFail bool) (string, bool) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	existing, found := ic.Get(key, value1)
	if !found {
		if addOnFail {
			ic.addLocked(key, value1, newValue2)
			return "", true
		}
		return "'" + key + "' with value1: '" + value1 + "' does not exist.", false
	}
	if set, ok := existing[key][value1]; ok {
		set.ForEach(func(value2 string) {
			ic.deleteLocked(key, value1, value2)
		})
	}
	ic.addLocked(key, value1, newValue2)
	return "", true
}

// putAssoc inserts subKey -> pos into the set stored under outerKey, creating
// the set if needed. Creation goes through GetOrPut so concurrent inserters for
// a brand-new outer key (the first two triples of a fresh subject in the
// forward tree, or the first two children of a fresh directory in the reverse
// tree) share one set. A Get-then-Put here used to let the second creator
// overwrite the first's set, dropping its entry from one index while the other
// index (whose outer key already existed) kept it — the forward/reverse
// divergence seen as phantoms and missing children on long-lived servers, and
// reproduced deterministically by the 8-worker load in TestLoadIndexConsistency.
func putAssoc(parent *lockedTree[uint64, *AssociationSet], outerKey uint64, subKey UniqueAssociation, pos int64) {
	parent.GetOrPut(outerKey, newAssociationSet).Put(subKey, pos)
}

func (ic *Collection) insertAssociation(level int, a *Triple, pos int64) {
	// HASH_LEVEL entries live in the dedicated hash association trees, which
	// always exist when blobPolicy is set; only real levels need growing.
	if level != HASH_LEVEL {
		ic.secureLevelInIndex(level)
	}
	if a.Value2Level != HASH_LEVEL {
		ic.secureLevelInIndex(a.Value2Level)
	}

	// Association trees store the int64 position in both modes (disk offset, or
	// arena index in memory mode), so nodes never box a Triple.
	fwd, _ := ic.associationsTree(level) // exists after secureLevelInIndex / hash store
	putAssoc(fwd, a.Key,
		UniqueAssociation{AssociateTo: a.Value2, Relation: a.Value1}, pos)

	atomic.AddUint64(&ic.totalAssociations, 1)

	// Insert reverse association (only for relations we index in reverse)
	if !ic.shouldBuildReverse(a) {
		return
	}
	rev, _ := ic.reverseAssociationsTree(a.Value2Level) // exists after secureLevelInIndex / hash store
	putAssoc(rev, a.Value2,
		UniqueAssociation{AssociateTo: a.Key, Relation: a.Value1}, pos)
}

func (ic *Collection) getAssociations(level int, entryID uint64) *AssociationSet {
	if tree, ok := ic.associationsTree(level); ok {
		if set, found := tree.Get(entryID); found {
			return set
		}
	}
	return newAssociationSet()
}

func (ic *Collection) getReverseAssociations(level int, entryID uint64) *AssociationSet {
	if tree, ok := ic.reverseAssociationsTree(level); ok {
		if set, found := tree.Get(entryID); found {
			return set
		}
	}
	return newAssociationSet()
}

func (ic *Collection) deleteAssociation(tree *AssociationSet, key UniqueAssociation, triplePos int64) {
	tree.Delete(key)

	if ic.writeToDisk {
		if ic.cache != nil && triplePos >= 0 {
			ic.cache.Evict(triplePos)
		}
		m := FileMod{
			Mode:     FILE_DELETE,
			Position: triplePos,
		}
		ic.dBWriteQueue <- m
	} else {
		ic.arenaFreeSlot(triplePos)
	}
}

func (ic *Collection) uniqueAssociationExists(keyLevel int, key uint64, value1 uint64, value2 uint64) bool {
	asses := ic.getAssociations(keyLevel, key)

	subkey := UniqueAssociation{
		Relation:    value1,
		AssociateTo: value2,
	}
	_, found := asses.Get(subkey)

	return found
}

// resolveTriple turns an association position into a Triple. In memory-only mode
// the position indexes into the arena. In disk-backed mode it is an on-disk
// offset served from the cache, falling back to a disk read.
func (ic *Collection) resolveTriple(pos int64) (Triple, bool) {
	if pos < 0 {
		return Triple{}, false
	}
	if !ic.writeToDisk {
		return ic.arenaLoad(pos)
	}
	if ic.cache != nil {
		if t, ok := ic.cache.Get(pos); ok {
			return t, true
		}
	}
	t, ok := ic.readTripleAt(pos)
	if ok && ic.cache != nil {
		ic.cache.Put(pos, t)
	}
	return t, ok
}

// resolveEntry resolves an index entry's position and verifies that the record
// it holds is the triple the entry stands for. An AssociationSet entry names
// the relation and one of the triple's two other members (value2 in a forward
// set, the subject in a reverse set) — the set does not know its own
// orientation, so a record is accepted when its relation matches and either
// member equals AssociateTo. A mismatch means the position was reused by
// another triple after a stale cache line or a lost delete: the cache line is
// evicted and the record re-read once from disk; if it still disagrees the
// entry is dropped (and logged), so a query never emits a triple that belongs
// to a different subject than the one the index claims.
func (ic *Collection) resolveEntry(ua UniqueAssociation, pos int64) (Triple, bool) {
	matches := func(t Triple) bool {
		return t.Value1 == ua.Relation && (t.Key == ua.AssociateTo || t.Value2 == ua.AssociateTo)
	}
	t, ok := ic.resolveTriple(pos)
	if !ok {
		return Triple{}, false
	}
	if matches(t) {
		return t, true
	}
	if ic.writeToDisk && ic.cache != nil {
		ic.cache.Evict(pos)
		if t, ok = ic.readTripleAt(pos); ok && matches(t) {
			ic.cache.Put(pos, t)
			return t, true
		}
	}
	slog.Warn("index entry does not match its record; skipping",
		"position", pos, "relation", ua.Relation, "associateTo", ua.AssociateTo,
		"recordKey", t.Key, "recordValue1", t.Value1, "recordValue2", t.Value2)
	return Triple{}, false
}

// readTripleAt reads and decodes the association record at pos from disk. The
// read is serviced by the writer goroutine, which owns the file handle; each
// call has its own reply channel, so concurrent queries do not serialize.
func (ic *Collection) readTripleAt(pos int64) (Triple, bool) {
	reply := make(chan Triple, 1)
	ic.dBReadQueue <- ReadRequest{Position: pos, ReplyChan: reply}
	t, ok := <-reply
	return t, ok
}

// SetValues makes (key, value1) hold exactly the given values: it removes every
// existing value2 for the relation and adds each of values. This is the
// multi-valued generalization of SimpleUpdate — the server-side "replace this
// relation" primitive, so clients no longer Get-then-Delete-each-then-Add.
// Passing an empty values slice clears the relation.
func (ic *Collection) SetValues(key string, value1 string, values []string) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	if existing, found := ic.Get(key, value1); found {
		if set, ok := existing[key][value1]; ok {
			set.ForEach(func(value2 string) {
				ic.deleteLocked(key, value1, value2)
			})
		}
	}
	for _, v := range values {
		ic.addLocked(key, value1, v)
	}
}

// ForEachTriple calls do for every forward triple in the collection. It walks
// each level's associations tree (keyed by entry ID) and runs each key's
// association subtree through the same Sort path used by Get, so the emitted
// triples match query results exactly. Intended for full-collection export.
func (ic *Collection) ForEachTriple(do func(StringTriple)) {
	// Use the set ForEach already hands us — re-fetching it via
	// getAssociations was a recursive RLock on the same shard and deadlocked
	// the collection against any concurrent Add of a new subject.
	emit := func(tree *lockedTree[uint64, *AssociationSet]) {
		tree.ForEach(func(_ uint64, set *AssociationSet) {
			triples, _ := ic.Sort(set, "", SortOptions{Limit: -1})
			for _, t := range triples {
				do(t)
			}
		})
	}

	ic.secureLevel.RLock()
	levelCount := ic.levelCount
	ic.secureLevel.RUnlock()
	for level := 0; level < levelCount; level++ {
		lvl, ok := ic.levelAt(level)
		if !ok {
			continue
		}
		emit(lvl.associations)
	}
	// Triples keyed by a hash live outside the per-level trees.
	if ic.hashAssociations != nil {
		emit(ic.hashAssociations)
	}
}
