package tiedb

import (
	"bytes"
	"cmp"
	"slices"
	"strconv"
	"sync/atomic"
)

// SetReverseRelations restricts which relations (value1) this collection indexes
// in reverse. Pass nil to index every relation (the default). Call before adding
// triples; it does not rebuild reverse indexes for triples already inserted.
func (ic *Collection) SetReverseRelations(relations []string) {
	if relations == nil {
		ic.reverseRelations = nil
		return
	}
	set := make(map[string]bool, len(relations))
	for _, r := range relations {
		set[r] = true
	}
	ic.reverseRelations = set
}

// SetBlobPolicy installs a whole-value blob policy on this collection and
// allocates the backing hash store. Pass nil to keep the trie-only behavior. See
// [[BlobPolicy]]. Call before values are inserted.
func (ic *Collection) SetBlobPolicy(p *BlobPolicy) {
	ic.blobPolicy = p
	if p == nil {
		ic.hashEntries = nil
		ic.hashValues = nil
		return
	}
	ic.hashEntries = newLockedTree[uint64, [32]byte](uint64Hash)
	ic.hashValues = newLockedTree[[32]byte, uint64](hashHash)
	ic.hashAssociations = newLockedTree[uint64, *AssociationSet](uint64Hash)
	ic.hashReverseAssociations = newLockedTree[uint64, *AssociationSet](uint64Hash)
}

// associationsTree returns the forward-association tree housing associations
// keyed by an entry at level: the per-level tree, or the dedicated hash tree for
// HASH_LEVEL entries. The bool is false when no such tree exists.
func (ic *Collection) associationsTree(level int) (*lockedTree[uint64, *AssociationSet], bool) {
	if level == HASH_LEVEL {
		return ic.hashAssociations, ic.hashAssociations != nil
	}
	if lvl, ok := ic.levelAt(level); ok {
		return lvl.associations, true
	}
	return nil, false
}

// reverseAssociationsTree is the reverse-index counterpart of associationsTree.
func (ic *Collection) reverseAssociationsTree(level int) (*lockedTree[uint64, *AssociationSet], bool) {
	if level == HASH_LEVEL {
		return ic.hashReverseAssociations, ic.hashReverseAssociations != nil
	}
	if lvl, ok := ic.levelAt(level); ok {
		return lvl.reverseAssociations, true
	}
	return nil, false
}

// insertHash stores raw under a fresh ID (or returns the existing ID when the
// value is already interned) and queues its TYPE_HASH record in disk mode. The
// caller holds changeMutex.
func (ic *Collection) insertHash(raw [32]byte) uint64 {
	if id, found := ic.hashValues.Get(raw); found {
		return id
	}
	id := ic.nextID()
	ic.hashEntries.Put(id, raw)
	ic.hashValues.Put(raw, id)
	if ic.writeToDisk {
		ic.dBWriteQueue <- FileMod{
			EntryType: TYPE_HASH,
			Mode:      FILE_ADD,
			EntryID:   id,
			HashValue: raw,
			Position:  ic.allocSlot(),
		}
	}
	return id
}

// loadHash reinserts a hash entry read from disk, keeping totalEntries in step
// with the highest seen ID (mirrors the TYPE_ENTRY load path).
func (ic *Collection) loadHash(entryID uint64, raw [32]byte) {
	ic.totalEntriesMutex.Lock()
	if entryID > ic.totalEntries {
		ic.totalEntries = entryID
	}
	ic.totalEntriesMutex.Unlock()
	ic.hashEntries.Put(entryID, raw)
	ic.hashValues.Put(raw, entryID)
}

// asBlob returns the raw bytes for value when the blob policy matches it and the
// bytes fit the fixed 32-byte hash slot. A policy match whose encoding is not 32
// bytes falls through to the trie rather than corrupting the fixed frame.
func (ic *Collection) asBlob(value string) ([32]byte, bool) {
	if ic.blobPolicy == nil {
		return [32]byte{}, false
	}
	raw, ok := ic.blobPolicy.Encode(value)
	if !ok || len(raw) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(raw), true
}

// shouldBuildReverse reports whether a triple's relation (value1) should get a
// reverse-association node. A nil reverseRelations set means index everything.
func (ic *Collection) shouldBuildReverse(a *Triple) bool {
	if ic.reverseRelations == nil {
		return true
	}
	return ic.reverseRelations[ic.getValueString(a.Value1Level, a.Value1)]
}

func (ic *Collection) Add(key string, value1 string, value2 string) {
	keyID, keyLevel := ic.insert(key)
	value1ID, value1Level := ic.insert(value1)
	value2ID, value2Level := ic.insert(value2)

	ass := Triple{
		Key:         keyID,
		Level:       keyLevel,
		Value1Level: value1Level,
		Value1:      value1ID,
		Value2Level: value2Level,
		Value2:      value2ID,
	}

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
	} else {
		pos = ic.arenaStore(&ass)
	}
	ic.insertAssociation(keyLevel, &ass, pos)

	if ic.writeToDisk {
		ic.finishedAdding.Add(1)
		ic.dBWriteQueue <- FileMod{
			EntryType: TYPE_ASSOCIATION,
			Level:     keyLevel,
			Mode:      FILE_ADD,
			Position:  pos,
			Triple:    &ass,
		}
	}
}

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

// firstValueOf returns the smallest value the key holds under relation, or ""
// if it holds none. Used by Sort's value-based ordering (SortByValue); picking
// the smallest keeps the ordering deterministic for multi-valued relations.
func (ic *Collection) firstValueOf(key, relation string) string {
	data, found := ic.Get(key, relation)
	if !found {
		return ""
	}
	values, ok := data[key][relation]
	if !ok {
		return ""
	}
	best := ""
	first := true
	values.ForEach(func(v string) {
		if first || v < best {
			best = v
			first = false
		}
	})
	return best
}

// firstNumberOf is firstValueOf for numeric ordering: the smallest value the key
// holds under relation that parses as a number, or ok=false if it holds none that
// do. It picks the numeric minimum rather than parsing the lexicographic minimum,
// so a multi-valued relation orders by the same value a reader would call its
// smallest.
func (ic *Collection) firstNumberOf(key, relation string) (float64, bool) {
	data, found := ic.Get(key, relation)
	if !found {
		return 0, false
	}
	values, ok := data[key][relation]
	if !ok {
		return 0, false
	}
	best := 0.0
	any := false
	values.ForEach(func(v string) {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return
		}
		if !any || n < best {
			best = n
			any = true
		}
	})
	return best, any
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

func (ic *Collection) Delete(key string, value1 string, value2 string) (string, bool) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	if asses, found := ic.GetAssociations(key); found {
		k, _, _ := ic.getEntryFromString(key)
		v1, _, f1 := ic.getEntryFromString(value1)
		v2, l2, f2 := ic.getEntryFromString(value2)

		if f1 && f2 {
			assKey := UniqueAssociation{
				AssociateTo: v2,
				Relation:    v1,
			}
			reverseAssKey := UniqueAssociation{
				AssociateTo: k,
				Relation:    v1,
			}

			if pos, found := asses.Get(assKey); found {
				ic.deleteAssociation(asses, assKey, pos)
				ic.getReverseAssociations(l2, v2).Delete(reverseAssKey)
				return "", true
			}
		}
		return "Did not find '" + value2 + "' with value1 '" + value1 + "'", false
	} else {
		return "Did not find '" + key + "'", false
	}
}

func (ic *Collection) Update(key string, value1 string, value2 string, newValue2 string) (string, bool) {
	if msg, ok := ic.Delete(key, value1, value2); ok {
		ic.Add(key, value1, newValue2)
		return "", true
	} else {
		return msg, false
	}
}

// UpdateAdd replaces value2 with newValue2, adding newValue2 even when the
// original (key, value1, value2) did not exist. It always succeeds; the message
// notes when a fallback add was used instead of an update.
func (ic *Collection) UpdateAdd(key string, value1 string, value2 string, newValue2 string) (string, bool) {
	if msg, ok := ic.Update(key, value1, value2, newValue2); ok {
		return msg, true
	} else {
		ic.Add(key, value1, newValue2)
		return msg + ": could not update; adding new value as requested.", true
	}
}

func (ic *Collection) Sync() {
	ic.finishedAdding.Wait()
}

// SimpleUpdate makes a scalar (single-valued) field equal to newValue2:
// it removes every existing value2 for (key, value1) and adds newValue2.
// When the field does not yet exist it adds newValue2 only if addOnFail is set.
// Use this for fields that are meant to hold exactly one value.
func (ic *Collection) SimpleUpdate(key string, value1 string, newValue2 string, addOnFail bool) (string, bool) {
	existing, found := ic.Get(key, value1)
	if !found {
		if addOnFail {
			ic.Add(key, value1, newValue2)
			return "", true
		}
		return "'" + key + "' with value1: '" + value1 + "' does not exist.", false
	}
	if set, ok := existing[key][value1]; ok {
		set.ForEach(func(value2 string) {
			ic.Delete(key, value1, value2)
		})
	}
	ic.Add(key, value1, newValue2)
	return "", true
}

func (ic *Collection) secureLevelInIndex(level int) {
	ic.secureLevel.RLock()
	enough := level < ic.levelCount
	ic.secureLevel.RUnlock()
	if enough {
		return
	}

	ic.secureLevel.Lock()
	defer ic.secureLevel.Unlock()

	for i := ic.levelCount; i <= level; i++ {
		ic.levels = append(ic.levels, entryLevel{
			entries:             newLockedTree[uint64, *UniqueValue](uint64Hash),
			uniqueValues:        newLockedTree[UniqueValue, uint64](uniqueValueHash),
			associations:        newLockedTree[uint64, *AssociationSet](uint64Hash),
			reverseAssociations: newLockedTree[uint64, *AssociationSet](uint64Hash),
		})
		ic.levelCount++
	}
}

// levelAt returns the entryLevel at level under a read lock, guarding against a
// concurrent secureLevelInIndex append reallocating the levels slice. The
// second return is false when level is out of range.
func (ic *Collection) levelAt(level int) (entryLevel, bool) {
	ic.secureLevel.RLock()
	defer ic.secureLevel.RUnlock()
	if level < 0 || level >= ic.levelCount {
		return entryLevel{}, false
	}
	return ic.levels[level], true
}

func (ic *Collection) valueExists(level int, parentId uint64, value [SIZE_VALUE]byte) (uint64, bool) {
	ic.secureLevelInIndex(level)

	lvl, ok := ic.levelAt(level)
	if !ok {
		return 0, false
	}
	if entryID, found := lvl.uniqueValues.Get(UniqueValue{parentId, value}); found {
		return entryID, true
	} else {
		return 0, false
	}
}

func (ic *Collection) insert(value string) (entryID uint64, level int) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	if raw, ok := ic.asBlob(value); ok {
		return ic.insertHash(raw), HASH_LEVEL
	}

	var lastParentID uint64
	bytes := []byte(value)
	checkExistance := true
	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var tmp uint64

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level = i / SIZE_VALUE

		value := ([SIZE_VALUE]byte)(bytes[i:end])

		if checkExistance {
			tmp, checkExistance = ic.valueExists(level, lastParentID, value)
		}
		if checkExistance {
			lastParentID = tmp
		} else {
			checkExistance = false
			lastParentID = ic.insertValue(level, lastParentID, value)
		}
	}

	return lastParentID, level
}

func (ic *Collection) getEntryFromString(value string) (entryID uint64, level int, found bool) {
	if raw, ok := ic.asBlob(value); ok {
		if id, exists := ic.hashValues.Get(raw); exists {
			return id, HASH_LEVEL, true
		}
		return 0, 0, false
	}

	bytes := []byte(value)

	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var lastParentId uint64
	var lastLevel int

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		value := ([SIZE_VALUE]byte)(bytes[i:end])

		if tmp, exists := ic.valueExists(level, lastParentId, value); exists {
			lastParentId = tmp
			lastLevel = level
		} else {
			return 0, 0, false
		}
	}

	return lastParentId, lastLevel, true
}

func (ic *Collection) GetTotalEntries() uint64 {
	return ic.totalEntries
}

func (ic *Collection) nextID() uint64 {
	return atomic.AddUint64(&ic.totalEntries, 1)
}

func (ic *Collection) insertValue(level int, parentId uint64, value [SIZE_VALUE]byte) (entryID uint64) {
	entryID = ic.nextID()
	uv := &UniqueValue{parentId, value}

	if ic.writeToDisk {
		m := FileMod{
			EntryType:   TYPE_ENTRY,
			Mode:        FILE_ADD,
			Level:       level,
			EntryID:     entryID,
			UniqueValue: uv,
			Position:    ic.allocSlot(),
		}
		ic.dBWriteQueue <- m
	}
	ic.insertEntry(level, entryID, uv)

	return entryID
}

func (ic *Collection) insertEntry(level int, id uint64, uv *UniqueValue) {
	ic.secureLevelInIndex(level)
	lvl, _ := ic.levelAt(level) // level exists after secureLevelInIndex
	lvl.entries.Put(id, uv)
	lvl.uniqueValues.Put(*uv, id)
}

// arenaStore places a Triple in the memory-mode arena and returns its position,
// reusing a freed slot when one is available.
func (ic *Collection) arenaStore(a *Triple) int64 {
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	if n := len(ic.arenaFree); n > 0 {
		pos := ic.arenaFree[n-1]
		ic.arenaFree = ic.arenaFree[:n-1]
		ic.arena[pos] = *a
		return pos
	}
	ic.arena = append(ic.arena, *a)
	return int64(len(ic.arena) - 1)
}

// arenaLoad reads the Triple at a memory-mode arena position.
func (ic *Collection) arenaLoad(pos int64) (Triple, bool) {
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	if pos < 0 || pos >= int64(len(ic.arena)) {
		return Triple{}, false
	}
	return ic.arena[pos], true
}

// freeSlot records a freed disk slot offset for reuse by a later allocSlot. It
// must only be called by the writer goroutine after the slot's tombstone has
// been written, so a reused slot is never handed out before its old contents are
// overwritten on disk.
func (ic *Collection) freeSlot(pos int64) {
	if pos < 0 {
		return
	}
	ic.freespaceMutex.Lock()
	ic.freespace = append(ic.freespace, pos)
	ic.freespaceMutex.Unlock()
}

// allocSlot reserves the disk offset for the next record: a reclaimed freespace
// slot if one exists, otherwise a fresh slot appended at the end of the file.
// Both the freespace stack and db_size are guarded here, so concurrent Adds
// never hand out the same offset. Callers pass the returned offset to the writer
// as FileMod.Position.
func (ic *Collection) allocSlot() int64 {
	ic.freespaceMutex.Lock()
	defer ic.freespaceMutex.Unlock()
	if n := len(ic.freespace); n > 0 {
		pos := ic.freespace[n-1]
		ic.freespace = ic.freespace[:n-1]
		return pos
	}
	pos := ic.db_size
	ic.db_size += ENTRY_SIZE
	return pos
}

// arenaFreeSlot returns a memory-mode arena slot for reuse.
func (ic *Collection) arenaFreeSlot(pos int64) {
	if pos < 0 {
		return
	}
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	ic.arenaFree = append(ic.arenaFree, pos)
}

func putAssoc(parent *lockedTree[uint64, *AssociationSet], outerKey uint64, subKey UniqueAssociation, pos int64) {
	if subTree, found := parent.Get(outerKey); found {
		subTree.Put(subKey, pos)
		return
	}
	subTree := newAssociationSet()
	subTree.Put(subKey, pos)
	parent.Put(outerKey, subTree)
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

func (ic *Collection) getUniqueValue(level int, id uint64) *UniqueValue {
	if lvl, ok := ic.levelAt(level); ok {
		if entry, found := lvl.entries.Get(id); found {
			return entry
		}
	}
	return nil
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

// Returns a copy of a full value
func (ic *Collection) getValue(level int, id uint64) []byte {
	if level < 0 {
		return []byte{}
	}
	if level == 0 {
		return ic.getUniqueValue(0, id).Value[:]
	}
	uv := ic.getUniqueValue(level, id)
	if uv != nil {
		return append(ic.getValue(level-1, uv.ParentId), uv.Value[:]...)
	}
	return nil
}

func (ic *Collection) getValueString(level int, entryID uint64) string {
	if level == HASH_LEVEL {
		if raw, found := ic.hashEntries.Get(entryID); found {
			return ic.blobPolicy.Decode(raw[:])
		}
		return ""
	}

	value := ic.getValue(level, entryID)
	value = bytes.Trim(value, "\x00")

	return string(value)
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

// readTripleAt reads and decodes the association record at pos from disk. The
// read is serviced by the writer goroutine, which owns the file handle; each
// call has its own reply channel, so concurrent queries do not serialize.
func (ic *Collection) readTripleAt(pos int64) (Triple, bool) {
	reply := make(chan Triple, 1)
	ic.dBReadQueue <- ReadRequest{Position: pos, ReplyChan: reply}
	t, ok := <-reply
	return t, ok
}

// loadTriples walks an association subtree and emits each Triple. It is a pure
// in-memory traversal: memory-only mode reads resident Triples, disk mode
// resolves positions through the cache/disk. No collection-wide serialization.
func (ic *Collection) loadTriples(tree *AssociationSet, tripleChan chan Triple) {
	tree.ForEach(func(_ UniqueAssociation, pos int64) {
		if t, ok := ic.resolveTriple(pos); ok {
			tripleChan <- t
		}
	})
	close(tripleChan)
}

func (ic *Collection) makeStringTriple(t Triple) StringTriple {
	st := StringTriple{}
	st.Key = ic.getValueString(t.Level, t.Key)
	st.Value1 = ic.getValueString(t.Value1Level, t.Value1)
	st.Value2 = ic.getValueString(t.Value2Level, t.Value2)

	return st
}

type SortOptions struct {
	Offset int
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
	c := make(chan Triple, 10000)
	go ic.loadTriples(tree, c)

	sorted = make([]StringTriple, 0, 1000)
	filterActive := value1Filter != ""
	for x := range c {
		t := ic.makeStringTriple(x)
		if filterActive && t.Value1 != value1Filter {
			continue
		}
		sorted = append(sorted, t)
	}

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
		t, ok := ic.resolveTriple(pos)
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
		attrs := make(map[string][]string)
		for _, t := range sorted {
			attrs[t.Value1] = append(attrs[t.Value1], t.Value2)
		}
		rows = append(rows, Row{Key: key, Attributes: attrs})
	}
	return rows
}

// SetValues makes (key, value1) hold exactly the given values: it removes every
// existing value2 for the relation and adds each of values. This is the
// multi-valued generalization of SimpleUpdate — the server-side "replace this
// relation" primitive, so clients no longer Get-then-Delete-each-then-Add.
// Passing an empty values slice clears the relation.
func (ic *Collection) SetValues(key string, value1 string, values []string) {
	if existing, found := ic.Get(key, value1); found {
		if set, ok := existing[key][value1]; ok {
			set.ForEach(func(value2 string) {
				ic.Delete(key, value1, value2)
			})
		}
	}
	for _, v := range values {
		ic.Add(key, value1, v)
	}
}

// ForEachTriple calls do for every forward triple in the collection. It walks
// each level's associations tree (keyed by entry ID) and runs each key's
// association subtree through the same Sort path used by Get, so the emitted
// triples match query results exactly. Intended for full-collection export.
func (ic *Collection) ForEachTriple(do func(StringTriple)) {
	emit := func(level int, tree *lockedTree[uint64, *AssociationSet]) {
		tree.ForEach(func(entryID uint64, _ *AssociationSet) {
			subtree := ic.getAssociations(level, entryID)
			triples, _ := ic.Sort(subtree, "", SortOptions{Limit: -1})
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
		emit(level, lvl.associations)
	}
	// Triples keyed by a hash live outside the per-level trees.
	if ic.hashAssociations != nil {
		emit(HASH_LEVEL, ic.hashAssociations)
	}
}

func (ic *Collection) closeDB() {
	// Signal the writer to drain any queued writes, sync, and close the fd, then
	// wait for it to exit (it calls finished.Done() on return). Do NOT close
	// dBWriteQueue: nothing ranges over it, and a closed channel would make the
	// writer's select case perpetually ready with a zero-value FileMod, racing
	// the shutdown signal into the "Wrong EntryType" panic.
	ic.dBCloseWriter <- true
	ic.finished.Wait()
}
