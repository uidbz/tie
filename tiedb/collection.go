package tiedb

import (
	"bytes"
	"fmt"
	"os"
	"sync/atomic"
)

func (ic *Collection) Collection(name string) *Collection {
	if ic.dBName == name {
		return ic
	}
	e := ic.insert(name)
	return ic.internalCollection(e.Level, e.Id)
}

func (ic *Collection) internalCollection(level int, id uint64) *Collection {
	if level == ic.level && id == ic.id {
		return ic
	}
	if col, found := ic.subCollections[level].Get(id); !found {
		subdir := ic.dBPath + "/" + ic.dBName + "-sub-collections"
		os.Mkdir(subdir, 0777)
		name := ic.getValueString(level, id)
		db := NewDB(ic.writeToDisk)
		ic2 := db.initialize(subdir, name, false)
		ic2.level = level
		ic2.id = id
		ic.subCollections[level].Put(id, ic2)

		return ic2
	} else {
		return col.(*Collection)
	}
}

func (ic *Collection) Add(key string, value1 string, value2 string) *Association {
	return ic.associateExt(key, ic.dBName, value1, ic.dBName, value2)
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
	if e, found := ic.getEntryFromString(value); !found {
		return &TieTree{}, false
	} else {
		return ic.getAssociationsFromEntry(e), true
	}
}

func (ic *Collection) GetAssociationsExt(value string, entryCollection string) (*TieTree, bool) {
	if c, found := ic.getEntryFromString(entryCollection); !found {
		return nil, false
	} else {
		col := ic.internalCollection(c.Level, c.Id)
		if e, found := col.getEntryFromString(value); !found {
			return nil, false
		} else {
			return ic.getAssociationsFromEntry(e), true
		}
	}
}

func (ic *Collection) Delete(key string, value1 string, value2 string) (string, bool) {
	if asses, found := ic.GetAssociations(key); found {
		e2, f1 := ic.getEntryFromString(value2)
		r, f2 := ic.getEntryFromString(value1)

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
				ic.deleteAssociation(asses, assKey, a.(*Association))
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
func (ic *Collection) SimpleUpdateUsingSet(key string, value1 string, newValue2 string, addOnFail bool, set *StringSliceSet) (string, bool) {
	if set != nil && set.Value1 != nil && set.Value2 != nil {
		if len(set.Value1) != len(set.Value2) {
			return "Assertion: Length of set.Value1 and set.Value2 must be equal", false
		}
		for i, x := range set.Value1 {
			if x == value1 {
				return ic.Update(key, value1, set.Value2[i], newValue2)
			}
		}
	}
	if addOnFail {
		ic.Add(key, value1, newValue2)
		return "", true
	}
	return "'" + key + "' with value1: '" + value1 + "' does not exist.", false
}

func (ic *Collection) secureLevelInIndex(level int) {
	levels := len(ic.entries)

	for i := levels; i <= level; i++ {
		ic.entryAdder = append(ic.entryAdder, make(chan *Entry, 1000))
		ic.associationAdder = append(ic.associationAdder, make(chan *Association, 1000))
		ic.associationExtAdder = append(ic.associationExtAdder, make(chan *AssociationExt, 1000))

		ic.entries = append(ic.entries, NewTreeWith(UInt64Comparator))
		ic.uniqueValues = append(ic.uniqueValues, NewTreeWith(UniqueValueComparator))
		ic.associations = append(ic.associations, NewTreeWith(UInt64Comparator))
		ic.associationsExt = append(ic.associationsExt, NewTreeWith(UniqueAssociationComparator))
		ic.subCollections = append(ic.subCollections, NewTreeWith(UInt64Comparator))

		go ic.insertEntryAdder(i)
		go ic.insertAssociationAdder(i)
		go ic.insertAssociationExtAdder(i)
	}
}

func (ic *Collection) valueExists(level int, parentId uint64, value []byte) (*Entry, bool) {
	ic.secureLevelInIndex(level)

	if len(value) != SIZE_VALUE {
		fmt.Println("Assertion: ValueExists: Expected", SIZE_VALUE, "bytes, got", len(value))
		return nil, false
	}
	valPtr, foundVal := ic.values.Get(value)
	if !foundVal {
		return nil, false
	}
	if entry, found := ic.uniqueValues[level].Get(UniqueValue{parentId, valPtr.(*[]byte)}); found {
		return entry.(*Entry), true
	} else {
		return nil, false
	}
}

func (ic *Collection) insert(value string) *Entry {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	var lastParent *Entry = &ic.root
	bytes := []byte(value)
	checkExistance := true
	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var tmp *Entry
	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		if checkExistance {
			tmp, checkExistance = ic.valueExists(level, lastParent.Id, bytes[i:end])
		}
		if checkExistance {
			lastParent = tmp
		} else {
			checkExistance = false
			lastParent = ic.insertValue(level, lastParent.Id, bytes[i:end])
		}
	}

	ic.sync() // Why was this here?

	return lastParent
}

func (ic *Collection) getEntryFromString(value string) (*Entry, bool) {
	bytes := []byte(value)

	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var lastParentId uint64
	var lastLevel int

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		if tmp, exists := ic.valueExists(level, lastParentId, bytes[i:end]); exists {
			lastParentId = tmp.Id
			lastLevel = level
		} else {
			return nil, false
		}
	}

	return ic.getEntry(lastLevel, lastParentId), true
}

func (ic *Collection) insertValue(level int, parentId uint64, value []byte) *Entry {
	tmp := make([]byte, SIZE_VALUE)
	copy(tmp, value) // This copy is extremely important!!
	// It will give very very hard to debug problems if it is not here
	// and if value is directly used as key.

	valPtr, found := ic.values.Get(tmp)
	if !found {
		ic.values.Put(tmp, &tmp)
		valPtr = &tmp
	}

	e := &Entry{Id: ic.nextID(),
		Level:       level,
		UniqueValue: UniqueValue{parentId, valPtr.(*[]byte)},
	}

	if ic.writeToDisk {
		m := FileMod{
			Mode:  FILE_APPEND,
			Entry: e,
		}
		if len(ic.freespace) > 0 {
			tmp := <-ic.freespace
			pos := tmp.getPosition()
			e.setPosition(pos)
			m.Mode = FILE_UPDATE
		}
		ic.dBWriteQueue <- m
	}
	ic.insertEntry(level, e)

	return e
}

func (t *Collection) insertEntryAdder(level int) {
	for e := range t.entryAdder[level] {
		t.entries[level].Put(e.Id, e)
		t.uniqueValues[level].Put(e.UniqueValue, e)
		// err := t.entries[level].Put(e.Id, e)
		// if err != nil {
		// 	fmt.Println("Error inserting entry:", err.Error())
		// }
		// if err = t.uniqueValues[level].put(e.UniqueValue, e); err != nil {
		// 	fmt.Println("Error inserting entry (value):", err.Error())
		// }

		t.inserterWG.Done()
	}
}

func (ic *Collection) insertAssociationAdder(level int) {
	for a := range ic.associationAdder[level] {
		s, found := ic.associations[level].Get(a.EntryId)
		if !found {
			ass := NewTreeWith(UniqueAssociationComparator)
			key := UniqueAssociation{
				AssociateTo: a.AssociateTo,
				Relation:    a.Relation,
			}
			ass.Put(key, a)
			ic.associations[level].Put(a.EntryId, ass)
			// err := ic.associations[level].put(a.EntryId, ass)
			// if err != nil {
			// 	fmt.Println("Error inserting Association:", err.Error())
			// }
		} else {
			ass := s.(*TieTree)
			key := UniqueAssociation{
				AssociateTo: a.AssociateTo,
				Relation:    a.Relation,
			}
			ass.Put(key, a)
		}

		atomic.AddUint64(&ic.totalAssociations, 1)
		ic.inserterWGAssociation.Done()
	}
}

func (ic *Collection) insertAssociationExtAdder(level int) {
	// fmt.Println("starting", level)
	// asses := ic.GetAssociationsFromEntry(e1)

	// // var keyExt UniqueAssociationExt

	// // if localIC {
	// if f, a := asses.Get(key); f {
	// 	return a.(*Association)
	// }
	for aExt := range ic.associationExtAdder[level] {
		id := UniqueAssociation{
			AssociateTo: aExt.AssociateTo,
			Relation:    aExt.Relation,
		}
		// if found, s := ic.Associations[level].Get(aExt.EntryId); found { // Only insert AssExt if Ass exists
		sExt, foundExt := ic.associationsExt[level].Get(id)
		if !foundExt {
			ass := NewTreeWith(UniqueAssociationExtComparator)
			key := UniqueAssociationExt{
				AssociateToCollection: aExt.AssociationCollection,
				AssociateTo:           aExt.AssociateTo,
				RelationCollection:    aExt.RelationCollection,
				Relation:              aExt.Relation,
			}
			ass.Put(key, aExt)
			ic.associationsExt[level].Put(id, ass)
			// err := ic.associationsExt[level].put(id, ass)
			// if err != nil {
			// 	fmt.Println("Error inserting Association:", err.Error())
			// }
		} else {
			ass := sExt.(*TieTree)
			key := UniqueAssociationExt{
				AssociateToCollection: aExt.AssociationCollection,
				AssociateTo:           aExt.AssociateTo,
				RelationCollection:    aExt.RelationCollection,
				Relation:              aExt.Relation,
			}
			ass.Put(key, aExt)
		}

		atomic.AddUint64(&ic.totalAssociations, 1)
		ic.inserterWGAssociationExt.Done()
	}
}

func (ic *Collection) insertEntry(level int, e *Entry) {
	ic.secureLevelInIndex(level)
	ic.inserterWG.Add(1)
	ic.entryAdder[level] <- e
}

func (ic *Collection) insertAssociation(level int, a *Association) {
	ic.secureLevelInIndex(level)
	ic.inserterWGAssociation.Add(1)
	ic.associationAdder[level] <- a
}

func (ic *Collection) insertAssociationExt(level int, a *AssociationExt) {
	ic.secureLevelInIndex(level)
	ic.inserterWGAssociationExt.Add(1)
	ic.associationExtAdder[level] <- a
}

func (ic *Collection) getEntry(level int, id uint64) *Entry {
	if level < 0 {
		return &ic.root
	}
	if level < len(ic.entries) {
		if entry, found := ic.entries[level].Get(id); found {
			return entry.(*Entry)
		}
	}
	return &ic.root
}

func (ic *Collection) getAssociationsFromEntry(e *Entry) *TieTree {
	if e.Level >= 0 && e.Level < len(ic.associations) {
		if set, found := ic.associations[e.Level].Get(e.Id); found {
			return set.(*TieTree)
		}
	}
	return NewTreeWith(UInt64Comparator)
}

func (ic *Collection) getAssociationsExtFromEntry(e *Entry) *TieTree {
	if e.Level >= 0 && e.Level < len(ic.associationsExt) {
		if set, found := ic.associationsExt[e.Level].Get(e.Id); found {
			return set.(*TieTree)
		}
	}
	return NewTreeWith(UInt64Comparator)
}

// Returns a copy of full value
func (ic *Collection) getValue(level int, id uint64) []byte {
	if level <= 0 {
		return *ic.getEntry(0, id).UniqueValue.Value
	}
	e := ic.getEntry(level, id)
	if e != nil {
		return append(ic.getValue(level-1, e.UniqueValue.ParentId), *e.UniqueValue.Value...)
	}
	return nil
}

func (ic *Collection) getValueString(level int, id uint64) string {
	value := ic.getValue(level, id)
	value = bytes.Trim(value, "\x00")

	return string(value)
}

func (ic *Collection) getValueStringFromEntry(e *Entry) string {
	value := ic.getValue(e.Level, e.Id)
	value = bytes.Trim(value, "\x00")
	return string(value)
}

func (ic *Collection) sync() {
	ic.inserterWG.Wait()
	ic.inserterWGAssociation.Wait()
	ic.inserterWGAssociationExt.Wait()
	ic.inserterWGDynTrie.Wait()
}

func (ic *Collection) deleteEntry(e *Entry) {
	ic.entries[e.Level].Delete(e.Id)

	if ic.writeToDisk {
		m := FileMod{
			Mode:  FILE_DELETE,
			Entry: e,
		}
		ic.dBWriteQueue <- m
	}
}

func (ic *Collection) deleteAssociation(tree *TieTree, key UniqueAssociation, a *Association) {
	tree.Delete(key)

	if ic.writeToDisk {
		m := FileMod{
			Mode:  FILE_DELETE,
			Entry: a,
		}
		ic.dBWriteQueue <- m
	}
}

// This is wrong. If the entry has any associations, they will no longer work.
// Cannot update the id, because the associations are linked to BOTH level + id
// func (ic *InternalCollection) UpdateEntry(oldEntry *Entry, newEntry *Entry) {
// 	newEntry.SetPosition(oldEntry.GetPosition())
// 	ic.DeleteEntry(oldEntry)
// 	ic.Sync()
// 	m := FileMod{
// 		Mode:  FILE_UPDATE,
// 		Entry: newEntry,
// 	}
// 	ic.DBWriteQueue <- m
// }

func (ic *Collection) getUniqueAssociation(key *Entry, value1 *Entry, value2 *Entry) (bool, *Association) {
	asses := ic.getAssociationsFromEntry(key)

	subkey := UniqueAssociation{
		AssociateTo: value1.Id,
		Relation:    value2.Id,
	}
	if a, found := asses.Get(subkey); found {
		return true, a.(*Association)
	}

	return false, nil
}

// TODO: Rename relation to value1, entry1 = key, entry2 = value2
func (ic *Collection) associateExt(entry1, relation_collection, relation, entry2_collection, entry2 string) *Association {
	e1 := ic.insert(entry1) // Key has been decided to be in current IC
	col2 := ic.Collection(entry2_collection)
	e2 := col2.insert(entry2)
	col3 := ic.Collection(relation_collection)
	r := ic.insert(relation)

	ass := Association{}
	ass.EntryId = e1.Id
	ass.Level = e1.Level
	ass.AssociationLevel = e2.Level
	ass.AssociateTo = e2.Id
	ass.RelationLevel = r.Level
	ass.Relation = r.Id

	if found, association := ic.getUniqueAssociation(e1, e2, r); found {
		return association
	}

	if ic.writeToDisk {
		m := FileMod{
			Mode:  FILE_APPEND,
			Entry: &ass,
		}
		if len(ic.freespace) > 0 {
			tmp := <-ic.freespace
			ass.setPosition(tmp.getPosition())
			m.Mode = FILE_UPDATE
		}
		ic.dBWriteQueue <- m
	}

	ic.insertAssociation(ass.Level, &ass)

	ic.sync() // Why was this here?

	localIC := (ic.dBName == entry2_collection) && (ic.dBName == relation_collection)
	if !localIC {
		assExt := AssociationExt{}
		assExt.AssociateTo = ass.AssociateTo
		assExt.Relation = ass.Relation
		assExt.RelationCollectionLevel = col3.level
		assExt.RelationCollection = col3.id
		assExt.AssociationCollectionLevel = col2.level
		assExt.AssociationCollection = col2.id

		if ic.writeToDisk {
			m := FileMod{
				Mode:  FILE_APPEND,
				Entry: &assExt,
			}
			if len(ic.freespace) > 0 {
				tmp := <-ic.freespace
				assExt.setPosition(tmp.getPosition())
				m.Mode = FILE_UPDATE
			}
			ic.dBWriteQueue <- m
		}

		ic.insertAssociationExt(assExt.RelationCollectionLevel, &assExt)

		ic.sync() // Why was this here?
	}

	return &ass
}

// func (ic *Collection) SetToString(value string, relationFilter string, s *Tree) (*StringSliceSet, []*Tree) {
// 	size := int(s.Size())

// 	set := StringSliceSet{Item: value,
// 		Key:    make([]string, size),
// 		Value2: make([]string, size),
// 		Value1: make([]string, size)}

// 	associationTrees := make([]*Tree, size)

// 	if size == 0 {
// 		return &set, nil
// 	}

// 	// start := time.Now()
// 	var wg sync.WaitGroup

// 	v := &ChanVisitor{}
// 	v.Ch = make(chan interface{}, 1000)
// 	wg.Add(size)
// 	go func() {
// 		i := 0
// 		for t := range v.Ch {
// 			x := t.(*Association)

// 			if i >= size { //In case results change since size was calculated
// 				for j := size; j <= i; j++ {
// 					set.Key = append(set.Key, "")
// 					set.Value2 = append(set.Value2, "")
// 					set.Value1 = append(set.Value1, "")
// 					associationTrees = append(associationTrees, &Tree{})
// 					wg.Add(1)
// 					size++
// 				}
// 			}
// 			set.Key[i] = ic.getValueString(x.Level, x.EntryId)
// 			set.Value2[i] = ic.getValueString(x.AssociationLevel, x.AssociateTo)
// 			associationTrees[i] = ic.getAssociationsFromEntry(ic.getEntry(x.AssociationLevel, x.AssociateTo))
// 			set.Value1[i] = ic.getValueString(x.RelationLevel, x.Relation)
// 			wg.Done()
// 			i++
// 		}
// 	}()
// 	s.Walk(v)
// 	wg.Wait()
// 	close(v.Ch)

// 	//Filters - probably should be done differently

// 	if relationFilter != "" {
// 		n := 0

// 		for i, x := range set.Value1 {
// 			if x == relationFilter {
// 				set.Value1[n] = x
// 				set.Value2[n] = set.Value2[i]
// 				associationTrees[n] = associationTrees[i]
// 				n++
// 			}
// 		}
// 		set.Value2 = set.Value2[:n]
// 		set.Value1 = set.Value1[:n]
// 		associationTrees = associationTrees[:n]
// 	}

// 	// fmt.Println("--- ", time.Since(start), " ---")
// 	return &set, associationTrees
// }

// New implementation of SetToString using new output format
func (ic *Collection) GetTripleSet(key string, value1Filter string, s *TieTree) (TripleSet, map[string]*TieTree) {
	result := make(TripleSet)

	c := make(chan *Association, 10000)
	go func() {
		it := s.Iterator()
		for it.Next() {
			c <- it.Value().(*Association)
		}
		close(c)
	}()

	val2trees := make(map[string]*TieTree)

	for x := range c {
		key := ic.getValueString(x.Level, x.EntryId)
		value1 := ic.getValueString(x.RelationLevel, x.Relation)
		value2 := ic.getValueString(x.AssociationLevel, x.AssociateTo)
		val2trees[value2] = ic.getAssociationsFromEntry(ic.getEntry(x.AssociationLevel, x.AssociateTo))

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
	ic.sync()
	levels := len(ic.entries)

	for i := 0; i < levels; i++ {
		close(ic.entryAdder[i])
		close(ic.associationAdder[i])
		close(ic.associationExtAdder[i])
	}
	ic.finished.Wait() // wait until finished writing
	ic.dBCloseWriter <- true
	close(ic.dBWriteQueue)
}

// func (ic *InternalCollection) GetCollectionFromString(value string) (bool, *InternalCollection) {
// 	e1 := ic.Insert(value)
// 	if e1 == nil {
// 		return false, nil
// 	}
// 	found, col := ic.SubCollections[e1.Level].Get(e1.Id)
// 	if !found {
// 		return true, ic.(Collection).Collection(value)
// 	}
// 	return true, col.(*SubCollectionEntry).Instance
// }

// func (ic *InternalCollection) GetCollection(level int, id uint64) (bool, *InternalCollection) {
// 	e1 := ic.Insert(level, id)
// 	if e1 == nil {
// 		return false, nil
// 	}
// 	found, col := ic.SubCollections[level].Get(e1.Id)
// 	if !found {
// 		return false, nil
// 		// return true, ic.(Collection).Collection(value)
// 	}
// 	return true, col.(*SubCollectionEntry).Instance
// }
