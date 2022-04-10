package tiedb

import (
	"bytes"
	"fmt"
	"os"
	"sync"
)

var mutex sync.Mutex

func (ic *InternalCollection) Collection(name string) Collection {
	return ic.InternalCollectionFromString(name)
}

func (ic *InternalCollection) InternalCollectionFromString(name string) *InternalCollection {
	if ic.DBName == name {
		return ic
	}
	e := ic.Insert(name)
	return ic.InternalCollection(e.Level, e.Id)
}

func (ic *InternalCollection) InternalCollection(level int, id uint64) *InternalCollection {
	if level == ic.Level && id == ic.Id {
		return ic
	}
	mutex.Lock()
	defer mutex.Unlock()
	if found, col := ic.SubCollections[level].Get(id); !found {
		subdir := ic.DBPath + "/" + ic.DBName + "-sub-collections"
		os.Mkdir(subdir, 0777)
		name := ic.GetValueString(level, id)
		ic2 := Initialize(subdir, name, false, ic.WriteToDisk)
		ic2.Level = level
		ic2.Id = id
		ic.SubCollections[level].Put(id, ic2)

		return ic2
	} else {
		return col.(*InternalCollection)
	}
}

func (ic *InternalCollection) SecureLevelInIndex(level int) {
	levels := len(ic.Entries)

	for i := levels; i <= level; i++ {
		ic.EntryAdder = append(ic.EntryAdder, make(chan *Entry, 1000))
		ic.AssociationAdder = append(ic.AssociationAdder, make(chan *Association, 1000))
		ic.AssociationExtAdder = append(ic.AssociationExtAdder, make(chan *AssociationExt, 1000))

		ic.Entries = append(ic.Entries, NewTreeWith(UInt64Comparator, ic.WriteToDisk))
		ic.UniqueValues = append(ic.UniqueValues, NewTreeWith(UniqueValueComparator, ic.WriteToDisk))
		ic.Associations = append(ic.Associations, NewTreeWith(UInt64Comparator, ic.WriteToDisk))
		ic.AssociationsExt = append(ic.AssociationsExt, NewTreeWith(UniqueAssociationComparator, ic.WriteToDisk))
		ic.SubCollections = append(ic.SubCollections, NewTreeWith(UInt64Comparator, ic.WriteToDisk))

		go ic.InsertEntryAdder(i)
		go ic.InsertAssociationAdder(i)
		go ic.InsertAssociationExtAdder(i)
	}
}

func (ic *InternalCollection) ValueExists(level int, parentId uint64, value []byte) (bool, *Entry) {
	ic.SecureLevelInIndex(level)

	if len(value) != SIZE_VALUE {
		fmt.Println("Assertion: ValueExists: Expected", SIZE_VALUE, "bytes, got", len(value))
		return false, nil
	}
	foundVal, valPtr := ic.Values.Get(value)
	if !foundVal {
		return false, nil
	}
	found, entry := ic.UniqueValues[level].Get(UniqueValue{parentId, valPtr.(*[]byte)})
	if !found {
		return false, nil
	} else {
		return found, entry.(*Entry)
	}
}

func (ic *InternalCollection) Insert(value string) *Entry {
	mutex.Lock()
	defer mutex.Unlock()

	var lastParent *Entry = &ic.Root
	bytes := []byte(value)
	checkExistance := true
	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var tmp *Entry
	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		if checkExistance {
			checkExistance, tmp = ic.ValueExists(level, lastParent.Id, bytes[i:end])
		}
		if checkExistance {
			lastParent = tmp
		} else {
			checkExistance = false
			lastParent = ic.InsertValue(level, lastParent.Id, bytes[i:end])
		}
	}

	ic.Sync()

	return lastParent
}

func (ic *InternalCollection) GetEntryFromString(value string) (bool, *Entry) {
	bytes := []byte(value)

	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var lastParentId uint64
	var lastLevel int

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		if exists, tmp := ic.ValueExists(level, lastParentId, bytes[i:end]); exists {
			lastParentId = tmp.Id
			lastLevel = level
		} else {
			return false, nil
		}
	}

	return true, ic.GetEntry(lastLevel, lastParentId)
}

func (ic *InternalCollection) InsertValue(level int, parentId uint64, value []byte) *Entry {
	tmp := make([]byte, SIZE_VALUE)
	copy(tmp, value) // This copy is extremely important!!
	// It will give very very hard to debug problems if it is not here
	// and if value is directly used as key.

	found, valPtr := ic.Values.Get(tmp)
	if !found {
		ic.Values.Put(tmp, &tmp)
		valPtr = &tmp
	}

	e := &Entry{Id: ic.NextID(),
		Level:       level,
		UniqueValue: UniqueValue{parentId, valPtr.(*[]byte)},
	}

	if ic.WriteToDisk {
		m := FileMod{
			Mode:  FILE_APPEND,
			Entry: e,
		}
		if len(ic.Freespace) > 0 {
			tmp := <-ic.Freespace
			pos := tmp.GetPosition()
			e.SetPosition(pos)
			m.Mode = FILE_UPDATE
		}
		ic.DBWriteQueue <- m
	}
	ic.InsertEntry(level, e)

	return e
}

func (t *InternalCollection) InsertEntryAdder(level int) {
	for e := range t.EntryAdder[level] {
		err := t.Entries[level].Put(e.Id, e)
		if err != nil {
			fmt.Println("Error inserting entry:", err.Error())
		}
		if err = t.UniqueValues[level].Put(e.UniqueValue, e); err != nil {
			fmt.Println("Error inserting entry (value):", err.Error())
		}

		t.InserterWG.Done()
	}
}

func (ic *InternalCollection) InsertAssociationAdder(level int) {
	for a := range ic.AssociationAdder[level] {
		found, s := ic.Associations[level].Get(a.EntryId)
		if !found {
			ass := NewTreeWith(UniqueAssociationComparator, ic.WriteToDisk)
			key := UniqueAssociation{
				AssociateTo: a.AssociateTo,
				Relation:    a.Relation,
			}
			ass.Put(key, a)
			err := ic.Associations[level].Put(a.EntryId, ass)
			if err != nil {
				fmt.Println("Error inserting Association:", err.Error())
			}
		} else {
			ass := s.(*Tree)
			key := UniqueAssociation{
				AssociateTo: a.AssociateTo,
				Relation:    a.Relation,
			}
			ass.Put(key, a)
		}

		ic.mu2.Lock()
		ic.TotalAsses = ic.TotalAsses + 1
		ic.InserterWGAssociation.Done()
		ic.mu2.Unlock()
	}
}

func (ic *InternalCollection) InsertAssociationExtAdder(level int) {
	// fmt.Println("starting", level)
	// asses := ic.GetAssociationsFromEntry(e1)

	// // var keyExt UniqueAssociationExt

	// // if localIC {
	// if f, a := asses.Get(key); f {
	// 	return a.(*Association)
	// }
	for aExt := range ic.AssociationExtAdder[level] {
		id := UniqueAssociation{
			AssociateTo: aExt.AssociateTo,
			Relation:    aExt.Relation,
		}
		// if found, s := ic.Associations[level].Get(aExt.EntryId); found { // Only insert AssExt if Ass exists
		foundExt, sExt := ic.AssociationsExt[level].Get(id)
		if !foundExt {
			ass := NewTreeWith(UniqueAssociationExtComparator, ic.WriteToDisk)
			key := UniqueAssociationExt{
				AssociateToCollection: aExt.AssociationCollection,
				AssociateTo:           aExt.AssociateTo,
				RelationCollection:    aExt.RelationCollection,
				Relation:              aExt.Relation,
			}
			ass.Put(key, aExt)
			err := ic.AssociationsExt[level].Put(id, ass)
			if err != nil {
				fmt.Println("Error inserting Association:", err.Error())
			}
		} else {
			ass := sExt.(*Tree)
			key := UniqueAssociationExt{
				AssociateToCollection: aExt.AssociationCollection,
				AssociateTo:           aExt.AssociateTo,
				RelationCollection:    aExt.RelationCollection,
				Relation:              aExt.Relation,
			}
			ass.Put(key, aExt)
		}

		ic.mu2.Lock()
		ic.TotalAsses = ic.TotalAsses + 1
		ic.InserterWGAssociationExt.Done()
		ic.mu2.Unlock()
	}
	// }
}

func (ic *InternalCollection) InsertEntry(level int, e *Entry) {
	ic.SecureLevelInIndex(level)
	ic.InserterWG.Add(1)
	ic.EntryAdder[level] <- e
}

func (ic *InternalCollection) InsertAssociation(level int, a *Association) {
	ic.SecureLevelInIndex(level)
	ic.InserterWGAssociation.Add(1)
	ic.AssociationAdder[level] <- a
}

func (ic *InternalCollection) InsertAssociationExt(level int, a *AssociationExt) {
	ic.SecureLevelInIndex(level)
	ic.InserterWGAssociationExt.Add(1)
	ic.AssociationExtAdder[level] <- a
}

func (ic *InternalCollection) GetEntry(level int, id uint64) *Entry {
	if level < 0 {
		return &ic.Root
	}
	if level < len(ic.Entries) {
		if ok, entry := ic.Entries[level].Get(id); ok {
			return entry.(*Entry)
		}
	}
	return &ic.Root
}

func (ic *InternalCollection) GetAssociations(value string) (bool, *Tree) {
	found, e := ic.GetEntryFromString(value)
	if !found {
		return false, &Tree{}
	}

	return true, ic.GetAssociationsFromEntry(e)
}

func (ic *InternalCollection) GetAssociationsExt(value string, entryCollection string) (bool, *Tree) {
	found, c := ic.GetEntryFromString(entryCollection)
	if !found {
		return false, nil
	}
	col := ic.InternalCollection(c.Level, c.Id)
	found2, e := col.GetEntryFromString(value)
	if !found2 {
		return false, nil
	}

	return true, ic.GetAssociationsFromEntry(e)
}

func (ic *InternalCollection) GetAssociationsFromEntry(e *Entry) *Tree {
	if e.Level >= 0 && e.Level < len(ic.Associations) {
		found, set := ic.Associations[e.Level].Get(e.Id)
		if found {
			return set.(*Tree)
		}
	}
	return NewTreeWith(UInt64Comparator, ic.WriteToDisk)
}

func (ic *InternalCollection) GetAssociationsExtFromEntry(e *Entry) *Tree {
	if e.Level >= 0 && e.Level < len(ic.AssociationsExt) {
		found, set := ic.AssociationsExt[e.Level].Get(e.Id)
		if found {
			return set.(*Tree)
		}
	}
	return NewTreeWith(UInt64Comparator, ic.WriteToDisk)
}

// Returns a copy of full value
func (ic *InternalCollection) GetValue(level int, id uint64) []byte {
	if level <= 0 {
		return *ic.GetEntry(0, id).UniqueValue.Value
	}
	e := ic.GetEntry(level, id)
	if e != nil {
		return append(ic.GetValue(level-1, e.UniqueValue.ParentId), *e.UniqueValue.Value...)
	}
	return nil
}

func (ic *InternalCollection) GetValueString(level int, id uint64) string {
	value := ic.GetValue(level, id)
	value = bytes.Trim(value, "\x00")

	return string(value)
}

func (ic *InternalCollection) GetValueStringFromEntry(e *Entry) string {
	value := ic.GetValue(e.Level, e.Id)
	value = bytes.Trim(value, "\x00")
	return string(value)
}

func (ic *InternalCollection) Sync() {
	ic.InserterWG.Wait()
	ic.InserterWGAssociation.Wait()
	ic.InserterWGAssociationExt.Wait()
	ic.InserterWGDynTrie.Wait()
}

func (ic *InternalCollection) Delete(key string, value1 string, value2 string) (bool, string) {
	found, asses := ic.GetAssociations(key)
	if found {
		f1, e2 := ic.GetEntryFromString(value2)
		f2, r := ic.GetEntryFromString(value1)

		if f1 && f2 {
			assKey := UniqueAssociation{
				// AssociateToCollection: ic.Id,
				AssociateTo: e2.Id,
				// RelationCollection:    ic.Id,
				Relation: r.Id,
			}
			if f, a := asses.Get(assKey); f {
				ic.DeleteAssociation(asses, assKey, a.(*Association))
				return true, ""
			}
		}
		return false, "Did not find '" + value2 + "' with value1 '" + value1 + "'"
	} else {
		return false, "Did not find '" + key + "'"
	}
}

func (ic *InternalCollection) Update(key string, value1 string, value2 string, newValue2 string) (bool, string) {
	if ok, msg := ic.Delete(key, value1, value2); ok {
		ic.Add(key, value1, newValue2)
		return true, ""
	} else {
		return false, msg
	}
}

// Try to update value2 to newvalue2. Add if unsuccessful. TODO: Add error checking
func (ic *InternalCollection) UpdateAdd(key string, value1 string, value2 string, newValue2 string) (bool, string) {
	if ok, msg := ic.Delete(key, value1, value2); ok {
		ic.Add(key, value1, newValue2)
		return true, ""
	} else {
		ic.Add(key, value1, newValue2)
		return true, msg + ": could not update; adding new value as requested."
	}
}

// Update first occurence of a value2. More expensive SimpleUpdateUsingSet - use that if udating many values
func (ic *InternalCollection) SimpleUpdate(key string, value1 string, newValue2 string, addOnFail bool) (bool, string) {
	found, set := ic.Get(key, value1)
	if found {
		if len(set.Value1) != len(set.Value2) {
			return false, "Assertion: Length of set.Value1 and set.Value2 must be equal"
		}
		for i, x := range set.Value1 {
			if x == value1 {
				return ic.Update(key, value1, set.Value2[i], newValue2)
			}
		}
	}
	if addOnFail {
		ic.Add(key, value1, newValue2)
		return true, ""
	}
	return false, "'" + key + "' with value1: '" + value1 + "' does not exist."
}

// Update first occurence of a value2
func (ic *InternalCollection) SimpleUpdateUsingSet(key string, value1 string, newValue2 string, addOnFail bool, set *StringSliceSet) (bool, string) {
	if set != nil && set.Value1 != nil && set.Value2 != nil {
		if len(set.Value1) != len(set.Value2) {
			return false, "Assertion: Length of set.Value1 and set.Value2 must be equal"
		}
		for i, x := range set.Value1 {
			if x == value1 {
				return ic.Update(key, value1, set.Value2[i], newValue2)
			}
		}
	}
	if addOnFail {
		ic.Add(key, value1, newValue2)
		return true, ""
	}
	return false, "'" + key + "' with value1: '" + value1 + "' does not exist."
}

func (ic *InternalCollection) DeleteEntry(e *Entry) {
	ic.Entries[e.Level].Delete(e.Id) //TODO: Make thread safe

	if ic.WriteToDisk {
		m := FileMod{
			Mode:  FILE_DELETE,
			Entry: e,
		}
		ic.DBWriteQueue <- m
	}
}

func (ic *InternalCollection) DeleteAssociation(tree *Tree, key UniqueAssociation, a *Association) {
	tree.Delete(key) //TODO: Make thread safe

	if ic.WriteToDisk {
		m := FileMod{
			Mode:  FILE_DELETE,
			Entry: a,
		}
		ic.DBWriteQueue <- m
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

func (ic *InternalCollection) Add(key string, value1 string, value2 string) *Association {
	ic.mutexAssociation.Lock()
	defer ic.mutexAssociation.Unlock()
	return ic.AssociateExt(key, ic.DBName, value1, ic.DBName, value2)
}

func (ic *InternalCollection) Get(key string, value1 string) (bool, *StringSliceSet) {
	found, tree := ic.GetAssociations(key)
	if found {
		data, _ := ic.SetToString(key, value1, tree)
		if data != nil {
			return true, data
		} else {
			return false, nil
		}
	} else {
		return false, nil
	}
}

func (ic *InternalCollection) GetUniqueAssociation(key *Entry, value1 *Entry, value2 *Entry) (bool, *Association) {
	asses := ic.GetAssociationsFromEntry(key)

	subkey := UniqueAssociation{
		AssociateTo: value1.Id,
		Relation:    value2.Id,
	}
	if f, a := asses.Get(subkey); f {
		return true, a.(*Association)
	}

	return false, nil
}

// TODO: Rename relation to value1, entry1 = key, entry2 = value2
func (ic *InternalCollection) AssociateExt(entry1, relation_collection, relation, entry2_collection, entry2 string) *Association {
	e1 := ic.Insert(entry1) // Key has been decided to be in current IC
	col2 := ic.InternalCollectionFromString(entry2_collection)
	e2 := col2.Insert(entry2)
	col3 := ic.InternalCollectionFromString(relation_collection)
	r := ic.Insert(relation)

	ass := Association{}
	ass.EntryId = e1.Id
	ass.Level = e1.Level
	ass.AssociationLevel = e2.Level
	ass.AssociateTo = e2.Id
	ass.RelationLevel = r.Level
	ass.Relation = r.Id

	if found, association := ic.GetUniqueAssociation(e1, e2, r); found {
		return association
	}

	if ic.WriteToDisk {
		m := FileMod{
			Mode:  FILE_APPEND,
			Entry: &ass,
		}
		if len(ic.Freespace) > 0 {
			tmp := <-ic.Freespace
			ass.SetPosition(tmp.GetPosition())
			m.Mode = FILE_UPDATE
		}
		ic.DBWriteQueue <- m
	}

	ic.InsertAssociation(ass.Level, &ass)

	ic.Sync()

	localIC := (ic.DBName == entry2_collection) && (ic.DBName == relation_collection)
	if !localIC {
		assExt := AssociationExt{}
		assExt.AssociateTo = ass.AssociateTo
		assExt.Relation = ass.Relation
		assExt.RelationCollectionLevel = col3.Level
		assExt.RelationCollection = col3.Id
		assExt.AssociationCollectionLevel = col2.Level
		assExt.AssociationCollection = col2.Id

		if ic.WriteToDisk {
			m := FileMod{
				Mode:  FILE_APPEND,
				Entry: &assExt,
			}
			if len(ic.Freespace) > 0 {
				tmp := <-ic.Freespace
				assExt.SetPosition(tmp.GetPosition())
				m.Mode = FILE_UPDATE
			}
			ic.DBWriteQueue <- m
		}

		ic.InsertAssociationExt(assExt.RelationCollectionLevel, &assExt)

		ic.Sync()
	}

	return &ass
}

func (ic *InternalCollection) SetToString(value string, relationFilter string, s *Tree) (*StringSliceSet, []*Tree) {
	size := int(s.Size())

	set := StringSliceSet{Item: value,
		Key:    make([]string, size),
		Value2: make([]string, size),
		Value1: make([]string, size)}

	associationTrees := make([]*Tree, size)

	if size == 0 {
		return &set, nil
	}

	// start := time.Now()
	var wg sync.WaitGroup

	v := &ChanVisitor{}
	v.Ch = make(chan interface{}, 1000)
	wg.Add(size)
	go func() {
		i := 0
		for t := range v.Ch {
			x := t.(*Association)

			if i >= size { //In case results change since size was calculated
				for j := size; j <= i; j++ {
					set.Key = append(set.Key, "")
					set.Value2 = append(set.Value2, "")
					set.Value1 = append(set.Value1, "")
					associationTrees = append(associationTrees, &Tree{})
					wg.Add(1)
					size++
				}
			}
			set.Key[i] = ic.GetValueString(x.Level, x.EntryId)
			set.Value2[i] = ic.GetValueString(x.AssociationLevel, x.AssociateTo)
			associationTrees[i] = ic.GetAssociationsFromEntry(ic.GetEntry(x.AssociationLevel, x.AssociateTo))
			set.Value1[i] = ic.GetValueString(x.RelationLevel, x.Relation)
			wg.Done()
			i++
		}
	}()
	s.Walk(v)
	wg.Wait()
	close(v.Ch)

	//Filters - probably should be done differently

	if relationFilter != "" {
		n := 0

		for i, x := range set.Value1 {
			if x == relationFilter {
				set.Value1[n] = x
				set.Value2[n] = set.Value2[i]
				associationTrees[n] = associationTrees[i]
				n++
			}
		}
		set.Value2 = set.Value2[:n]
		set.Value1 = set.Value1[:n]
		associationTrees = associationTrees[:n]
	}

	// fmt.Println("--- ", time.Since(start), " ---")
	return &set, associationTrees
}

func (ic *InternalCollection) CloseDB() {
	ic.Sync()
	levels := len(ic.Entries)

	for i := 0; i < levels; i++ {
		close(ic.EntryAdder[i])
		close(ic.AssociationAdder[i])
		close(ic.AssociationExtAdder[i])
	}
	ic.Finished.Wait() // wait until finished writing
	ic.DBCloseWriter <- true
	close(ic.DBWriteQueue)
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
