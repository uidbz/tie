package tiedb

import (
	"bytes"
	"cmp"
	"slices"
	"sync/atomic"
)

func (ic *Collection) Add(key string, value1 string, value2 string) {
	ic.finishedAdding.Add(1)

	k, keyLevel := ic.insert(key)
	v1, value1Level := ic.insert(value1)
	v2, value2Level := ic.insert(value2)

	ass := Triple{
		Key:         k.Id,
		Level:       keyLevel,
		Value1Level: value1Level,
		Value1:      v1.Id,
		Value2Level: value2Level,
		Value2:      v2.Id,
	}

	if ic.uniqueAssociationExists(keyLevel, k, v1, v2) {
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
	if e, level, found := ic.getEntryFromString(value); !found {
		return &TieTree{}, false
	} else {
		return ic.getAssociationsFromEntry(level, e), true
	}
}

func (ic *Collection) GetReverseAssociations(value string) (*TieTree, bool) {
	if e, level, found := ic.getEntryFromString(value); !found {
		return &TieTree{}, false
	} else {
		return ic.getReverseAssociationsFromEntry(level, e), true
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
				AssociateTo: v2.Id,
				Relation:    v1.Id,
			}
			reverseAssKey := UniqueAssociation{
				AssociateTo: k.Id,
				Relation:    v1.Id,
			}

			if a, found := asses.Get(assKey); found {
				ic.deleteAssociation(asses, assKey, a.(int64))
				ic.getReverseAssociationsFromEntry(l2, v2).Delete(reverseAssKey)
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

// Update first occurence of a value2. More expensive SimpleUpdateUsingSet - use that if udating many values
func (ic *Collection) SimpleUpdate(key string, value1 string, newValue2 string, addOnFail bool) (string, bool) {
	// found, set := ic.Get(key, value1)
	// if found {
	// 	// TODO: Fix
	// 	// if len(set.Value1) != len(set.Value2) { // TODO: Understand what was the idea behind this. Don't remember.
	// 	// 	return false, "Assertion: Length of set.Value1 and set.Value2 must be equal"
	// 	// }
	// 	// if (*set)[key][value1][]
	// 	// ic.Update(key, value1, (*set)[key][value1], newValue2)
	// 	// for i, x := range set.Value1 {
	// 	// 	if x == value1 {
	// 	// 		return ic.Update(key, value1, set.Value2[i], newValue2)
	// 	// 	}
	// 	// }
	// }
	// if addOnFail {
	// 	ic.Add(key, value1, newValue2)
	// 	return true, ""
	// }
	return "'" + key + "' with value1: '" + value1 + "' does not exist.", false
}

// Update first occurence of a value2
// func (ic *Collection) SimpleUpdateUsingSet(key string, value1 string, newValue2 string, addOnFail bool, set *StringSliceSet) (string, bool) {
// 	if set != nil && set.Value1 != nil && set.Value2 != nil {
// 		if len(set.Value1) != len(set.Value2) {
// 			return "Assertion: Length of set.Value1 and set.Value2 must be equal", false
// 		}
// 		for i, x := range set.Value1 {
// 			if x == value1 {
// 				return ic.Update(key, value1, set.Value2[i], newValue2)
// 			}
// 		}
// 	}
// 	if addOnFail {
// 		ic.Add(key, value1, newValue2)
// 		return "", true
// 	}
// 	return "'" + key + "' with value1: '" + value1 + "' does not exist.", false
// }

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

func (ic *Collection) valueExists(level int, parentId uint64, value [SIZE_VALUE]byte) (*Entry, bool) {
	ic.secureLevelInIndex(level)

	if entry, found := ic.levels[level].uniqueValues.Get(UniqueValue{parentId, value}); found {
		return entry.(*Entry), true
	} else {
		return nil, false
	}
}

func (ic *Collection) insert(value string) (parent *Entry, level int) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	lastParent := &Entry{}
	bytes := []byte(value)
	checkExistance := true
	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var tmp *Entry

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level = i / SIZE_VALUE

		value := ([SIZE_VALUE]byte)(bytes[i:end])

		if checkExistance {
			tmp, checkExistance = ic.valueExists(level, lastParent.Id, value)
		}
		if checkExistance {
			lastParent = tmp
		} else {
			checkExistance = false
			lastParent = ic.insertValue(level, lastParent.Id, value)
		}
	}

	return lastParent, level
}

func (ic *Collection) getEntryFromString(value string) (entry *Entry, level int, found bool) {
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
			lastParentId = tmp.Id
			lastLevel = level
		} else {
			return nil, 0, false
		}
	}

	return ic.getEntry(lastLevel, lastParentId), lastLevel, true
}

func (ic *Collection) insertValue(level int, parentId uint64, value [SIZE_VALUE]byte) *Entry {
	e := &Entry{Id: ic.nextID(),
		UniqueValue: UniqueValue{parentId, value},
	}

	if ic.writeToDisk {
		m := FileMod{
			EntryType: TYPE_ENTRY,
			Mode:      FILE_ADD,
			Level:     level,
			Entry:     e,
		}
		ic.dBWriteQueue <- m
	}
	ic.insertEntry(level, e)

	return e
}

func (ic *Collection) insertEntry(level int, e *Entry) {
	ic.secureLevelInIndex(level)
	ic.levels[level].entries.Put(e.Id, e)
	ic.levels[level].uniqueValues.Put(e.UniqueValue, e)
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

	// Insert reverse association
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

func (ic *Collection) getEntry(level int, id uint64) *Entry {
	if level < 0 {
		return nil
	}
	if level < ic.levelCount {
		if entry, found := ic.levels[level].entries.Get(id); found {
			return entry.(*Entry)
		}
	}
	return nil
}

func (ic *Collection) getAssociationsFromEntry(level int, e *Entry) *TieTree {
	if level >= 0 && level < ic.levelCount {
		if set, found := ic.levels[level].associations.Get(e.Id); found {
			return set.(*TieTree)
		}
	}
	return NewTreeWith(UInt64Comparator)
}

func (ic *Collection) getReverseAssociationsFromEntry(level int, e *Entry) *TieTree {
	if level >= 0 && level < ic.levelCount {
		if set, found := ic.levels[level].reverseAssociations.Get(e.Id); found {
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
		return ic.getEntry(0, id).UniqueValue.Value[:]
	}
	e := ic.getEntry(level, id)
	if e != nil {
		return append(ic.getValue(level-1, e.UniqueValue.ParentId), e.UniqueValue.Value[:]...)
	}
	return nil
}

func (ic *Collection) getValueString(level int, id uint64) string {
	value := ic.getValue(level, id)
	value = bytes.Trim(value, "\x00")

	return string(value)
}

func (ic *Collection) getValueStringFromEntry(level int, e *Entry) string {
	value := ic.getValue(level, e.Id)
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

func (ic *Collection) uniqueAssociationExists(keyLevel int, key *Entry, value1 *Entry, value2 *Entry) bool {
	asses := ic.getAssociationsFromEntry(keyLevel, key)

	subkey := UniqueAssociation{
		Relation:    value1.Id,
		AssociateTo: value2.Id,
	}
	_, found := asses.Get(subkey)

	return found
}

func (ic *Collection) loadTriples(tree *TieTree, tripleChan chan Triple) {
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

	if count < o.Offset {
		return make([]StringTriple, 0), count
	}

	if o.Limit < 0 || count < o.Offset+o.Limit {
		return sorted[o.Offset:], count
	}

	return sorted[o.Offset : o.Offset+o.Limit], count
}

func (ic *Collection) SortByNextLevelOne(tree *TieTree, value1Filter string, o SortOptions) (sorted []StringTriple) {

	// TODO
	return []StringTriple{}
}

func (ic *Collection) GetValue2Trees(s *TieTree, value1Filter string) (value2Trees map[string]*TieTree) {
	value2Trees = make(map[string]*TieTree)

	c := make(chan Triple, 10000)
	go ic.loadTriples(s, c)

	filterActive := value1Filter != ""

	for x := range c {
		t := ic.makeStringTriple(x)
		if filterActive && t.Value1 != value1Filter {
			continue
		}
		if _, ok := value2Trees[t.Value2]; !ok {
			value2Trees[t.Value2] = ic.getAssociationsFromEntry(x.Level, ic.getEntry(x.Value2Level, x.Value2))
		}
	}

	return value2Trees
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

func (ic *Collection) closeDB() {
	ic.finished.Wait() // wait until finished writing
	ic.dBCloseWriter <- true
	close(ic.dBWriteQueue)
}
