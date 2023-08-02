package tiedb

import (
	"bytes"
	"fmt"
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
		data, _ := ic.GetTripleSet(key, value1, tree)
		if data != nil {
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

func (ic *Collection) Delete(key string, value1 string, value2 string) (string, bool) {
	if asses, found := ic.GetAssociations(key); found {
		e2, _, f1 := ic.getEntryFromString(value2)
		r, _, f2 := ic.getEntryFromString(value1)

		if f1 && f2 {
			assKey := UniqueAssociation{
				// AssociateToCollection: ic.Id,
				AssociateTo: e2.Id,
				// RelationCollection:    ic.Id,
				Relation: r.Id,
			}
			// I believe the below Get and deleteAssociation has to happen as 1 operation.
			// in case a time slice happen after ;found
			ic.changeMutex.Lock()
			defer ic.changeMutex.Unlock()

			if a, found := asses.Get(assKey); found {
				ic.deleteAssociation(asses, assKey, a.(int64))
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
	ic.secureLevel.Lock()
	defer ic.secureLevel.Unlock()

	levels := len(ic.entries)

	for i := levels; i <= level; i++ {
		ic.entries = append(ic.entries, NewTreeWith(UInt64Comparator))
		ic.uniqueValues = append(ic.uniqueValues, NewTreeWith(UniqueValueComparator))
		ic.associations = append(ic.associations, NewTreeWith(UInt64Comparator))
	}
}

func (ic *Collection) valueExists(level int, parentId uint64, value [SIZE_VALUE]byte) (*Entry, bool) {
	ic.secureLevelInIndex(level)

	if entry, found := ic.uniqueValues[level].Get(UniqueValue{parentId, value}); found {
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
	ic.entries[level].Put(e.Id, e)
	ic.uniqueValues[level].Put(e.UniqueValue, e)
}

func (ic *Collection) insertAssociation(level int, a *Triple, pos int64) {
	ic.secureLevelInIndex(level)
	s, found := ic.associations[level].Get(a.Key)
	if !found {
		ass := NewTreeWith(UniqueAssociationComparator)
		key := UniqueAssociation{
			AssociateTo: a.Value2,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
		ic.associations[level].Put(a.Key, ass)
	} else {
		ass := s.(*TieTree)
		key := UniqueAssociation{
			AssociateTo: a.Value2,
			Relation:    a.Value1,
		}
		ass.Put(key, pos)
	}

	atomic.AddUint64(&ic.totalAssociations, 1)
}

func (ic *Collection) getEntry(level int, id uint64) *Entry {
	if level < 0 {
		return nil
	}
	if level < len(ic.entries) {
		if entry, found := ic.entries[level].Get(id); found {
			return entry.(*Entry)
		}
	}
	fmt.Println(level, id)
	return nil
}

func (ic *Collection) getAssociationsFromEntry(level int, e *Entry) *TieTree {
	if level >= 0 && level < len(ic.associations) {
		if set, found := ic.associations[level].Get(e.Id); found {
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

// New implementation of SetToString using new output format
func (ic *Collection) GetTripleSet(key string, value1Filter string, s *TieTree) (TripleSet, map[string]*TieTree) {
	result := make(TripleSet)

	c := make(chan Triple, 10000)
	go func() {
		it := s.Iterator()
		for it.Next() {
			pos := it.Value().(int64)
			if pos != -1 {
				ic.dbReadWg.Add(1)
				ic.dBReadQueue <- ReadRequest{
					Position:  it.Value().(int64),
					ReplyChan: c,
				}
			}
		}
		ic.dbReadWg.Wait()
		close(c)
	}()

	val2trees := make(map[string]*TieTree)

	for x := range c {
		key := ic.getValueString(x.Level, x.Key)
		value1 := ic.getValueString(x.Value1Level, x.Value1)
		value2 := ic.getValueString(x.Value2Level, x.Value2)
		val2trees[value2] = ic.getAssociationsFromEntry(x.Level, ic.getEntry(x.Value2Level, x.Value2))

		if !result.Has(key) {
			result[key] = make(Value1)
		}
		if !result[key].Has(value1) {
			result[key][value1] = make(Value2)
		}
		if !result[key][value1].Has(value2) {
			result[key][value1][value2] = Unit{}
		}
	}

	//Filters - probably should be done differently
	if value1Filter != "" {
		val1 := result[value1Filter]
		result := make(TripleSet)
		result[key] = make(Value1)
		result[key] = val1
	}

	return result, val2trees
}

func (ic *Collection) closeDB() {
	ic.finished.Wait() // wait until finished writing
	ic.dBCloseWriter <- true
	close(ic.dBWriteQueue)
}
