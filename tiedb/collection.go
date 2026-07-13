package tiedb

import (
	"bytes"
	"cmp"
	"slices"
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

// shouldBuildReverse reports whether a triple's relation (value1) should get a
// reverse-association node. A nil reverseRelations set means index everything.
func (ic *Collection) shouldBuildReverse(a *Triple) bool {
	if ic.reverseRelations == nil {
		return true
	}
	return ic.reverseRelations[ic.getValueString(a.Value1Level, a.Value1)]
}

func (ic *Collection) Add(key string, value1 string, value2 string) {
	ic.finishedAdding.Add(1)

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
		ic.finishedAdding.Done()
		return
	} else {
		ic.insertAssociation(keyLevel, &ass, -1) // Position will be updated after being written to disk
	}

	if ic.writeToDisk {
		m := FileMod{
			EntryType:   TYPE_ASSOCIATION,
			Level:       keyLevel,
			Mode:        FILE_ADD,
			Association: &ass,
		}
		ic.dBWriteQueue <- m
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

func (ic *Collection) GetAssociations(value string) (*TieTree, bool) {
	if entryID, level, found := ic.getEntryFromString(value); !found {
		return &TieTree{}, false
	} else {
		return ic.getAssociations(level, entryID), true
	}
}

func (ic *Collection) GetReverseAssociations(value string) (*TieTree, bool) {
	if entryID, level, found := ic.getEntryFromString(value); !found {
		return &TieTree{}, false
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

			if a, found := asses.Get(assKey); found {
				ic.deleteAssociation(asses, assKey, a.(int64))
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

// Try to update value2 to newvalue2. Add if unsuccessful. TODO: Add error checking
func (ic *Collection) UpdateAdd(key string, value1 string, value2 string, newValue2 string) (string, bool) {
	if msg, ok := ic.Delete(key, value1, value2); ok {
		ic.Add(key, value1, newValue2)
		return "", true
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
	if level < ic.levelCount {
		return
	}

	ic.secureLevel.Lock()
	defer ic.secureLevel.Unlock()

	for i := ic.levelCount; i <= level; i++ {
		ic.levels = append(ic.levels, entryLevel{
			entries:             NewTreeWith(UInt64Comparator),
			uniqueValues:        NewTreeWith(UniqueValueComparator),
			associations:        NewTreeWith(UInt64Comparator),
			reverseAssociations: NewTreeWith(UInt64Comparator),
		})
		ic.levelCount++
	}
}

func (ic *Collection) valueExists(level int, parentId uint64, value [SIZE_VALUE]byte) (uint64, bool) {
	ic.secureLevelInIndex(level)

	if entryID, found := ic.levels[level].uniqueValues.Get(&UniqueValue{parentId, value}); found {
		return entryID.(uint64), true
	} else {
		return 0, false
	}
}

func (ic *Collection) insert(value string) (entryID uint64, level int) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

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
		}
		ic.dBWriteQueue <- m
	}
	ic.insertEntry(level, entryID, uv)

	return entryID
}

func (ic *Collection) insertEntry(level int, id uint64, uv *UniqueValue) {
	ic.secureLevelInIndex(level)
	ic.levels[level].entries.Put(id, uv)
	ic.levels[level].uniqueValues.Put(uv, id)
}

func (ic *Collection) insertAssociation(level int, a *Triple, pos int64) {
	if level > a.Value2Level {
		ic.secureLevelInIndex(level)
	} else {
		ic.secureLevelInIndex(a.Value2Level)
	}
	assTree, found := ic.levels[level].associations.Get(a.Key)
	if !found {
		ass := NewTreeWith(UniqueAssociationComparator)
		key := UniqueAssociation{
			AssociateTo: a.Value2,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
		ic.levels[level].associations.Put(a.Key, ass)
	} else {
		ass := assTree.(*TieTree)
		key := UniqueAssociation{
			AssociateTo: a.Value2,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
	}

	atomic.AddUint64(&ic.totalAssociations, 1)

	// Insert reverse association (only for relations we index in reverse)
	if !ic.shouldBuildReverse(a) {
		return
	}
	reverseAssTree, found := ic.levels[a.Value2Level].reverseAssociations.Get(a.Value2)
	if !found {
		ass := NewTreeWith(UniqueAssociationComparator)
		key := UniqueAssociation{
			AssociateTo: a.Key,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
		ic.levels[a.Value2Level].reverseAssociations.Put(a.Value2, ass)
	} else {
		ass := reverseAssTree.(*TieTree)
		key := UniqueAssociation{
			AssociateTo: a.Key,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
	}
}

func (ic *Collection) getUniqueValue(level int, id uint64) *UniqueValue {
	if level < 0 {
		return nil
	}
	if level < ic.levelCount {
		if entry, found := ic.levels[level].entries.Get(id); found {
			return entry.(*UniqueValue)
		}
	}
	return nil
}

func (ic *Collection) getAssociations(level int, entryID uint64) *TieTree {
	if level >= 0 && level < ic.levelCount {
		if set, found := ic.levels[level].associations.Get(entryID); found {
			return set.(*TieTree)
		}
	}
	return NewTreeWith(UInt64Comparator)
}

func (ic *Collection) getReverseAssociations(level int, entryID uint64) *TieTree {
	if level >= 0 && level < ic.levelCount {
		if set, found := ic.levels[level].reverseAssociations.Get(entryID); found {
			return set.(*TieTree)
		}
	}
	return NewTreeWith(UInt64Comparator)
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
	value := ic.getValue(level, entryID)
	value = bytes.Trim(value, "\x00")

	return string(value)
}

func (ic *Collection) deleteAssociation(tree *TieTree, key UniqueAssociation, triplePos int64) {
	tree.Delete(key)

	if ic.writeToDisk {
		m := FileMod{
			Mode:     FILE_DELETE,
			Position: triplePos,
		}
		ic.dBWriteQueue <- m
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

func (ic *Collection) loadTriples(tree *TieTree, tripleChan chan Triple) {
	ic.loadingTriples.Lock() // because dbReadwg.Wait() cannot be called multiple times
	defer ic.loadingTriples.Unlock()

	it := tree.Iterator()
	for it.Next() {
		pos := it.Value().(int64)
		if pos != -1 {
			ic.dbReadWg.Add(1)
			ic.dBReadQueue <- ReadRequest{
				Position:  it.Value().(int64),
				ReplyChan: tripleChan,
			}
		}
	}
	ic.dbReadWg.Wait()
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
}

func (ic *Collection) Sort(tree *TieTree, value1Filter string, o SortOptions) (sorted []StringTriple, totalCount int) {
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

	slices.SortFunc(sorted, func(a, b StringTriple) int {
		if o.SortBy == "" {
			if n := cmp.Compare(a.Key, b.Key); n != 0 {
				return n
			}
			if n := cmp.Compare(a.Value1, b.Value1); n != 0 {
				return n
			}
			return cmp.Compare(a.Value2, b.Value2)
		} else {
			if a.Value1 != o.SortBy {
				return 1
			}
			if a.Value1 == o.SortBy && b.Value1 != o.SortBy {
				return -1
			}
			if n := cmp.Compare(a.Key, b.Key); n != 0 {
				return n
			}
			return cmp.Compare(a.Value2, b.Value2)
		}
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

func (ic *Collection) GetTripleSet(s *TieTree, value1Filter string, o SortOptions) (result TripleSet, totalCount int) {
	result = make(TripleSet)

	tripleSlice, totalCount := ic.Sort(s, value1Filter, o)

	for _, t := range tripleSlice {
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

	return result, totalCount
}

// ForEachTriple calls do for every forward triple in the collection. It walks
// each level's associations tree (keyed by entry ID) and runs each key's
// association subtree through the same Sort path used by Get, so the emitted
// triples match query results exactly. Intended for full-collection export.
func (ic *Collection) ForEachTriple(do func(StringTriple)) {
	for level := 0; level < ic.levelCount; level++ {
		it := ic.levels[level].associations.Iterator()
		for it.Next() {
			entryID := it.Key().(uint64)
			subtree := ic.getAssociations(level, entryID)
			triples, _ := ic.Sort(subtree, "", SortOptions{Limit: -1})
			for _, t := range triples {
				do(t)
			}
		}
	}
}

func (ic *Collection) closeDB() {
	ic.finished.Wait() // wait until finished writing
	ic.dBCloseWriter <- true
	close(ic.dBWriteQueue)
}
