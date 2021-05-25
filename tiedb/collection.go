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
		return false, "Did not find '" + value2 + "' with relation '" + value1 + "'"
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

// func (ic *InternalCollection) SetToString(value string, s *Tree) *StringSliceSet {
// 	return SetToString(value, s, ic, GetMainCollection)
// }

// TODO: Bug - does not return if no associations
// func SetToString(value string, s *Tree, ic *InternalCollection, cf CollectionFunc) *StringSliceSet {
// func (ic *InternalCollection) SetToString(value string, s *Tree) *StringSliceSet {
// 	, valueCollection string,
// }
func (ic *InternalCollection) SetToString(value string, relationFilter string, s *Tree) (*StringSliceSet, []*Tree) {
	size := s.Size()
	// var e *Entry
	// if valueCollection == nil {
	// e := ic.GetEntryFromString(value)
	// }

	set := StringSliceSet{Item: value, //ic.GetValueStringFromEntry(e),
		Keys:         make([]string, size),
		Associations: make([]string, size),
		Relations:    make([]string, size)}

	associationTrees := make([]*Tree, size)

	if size == 0 {
		return &set, nil // TODO: Test if this fixes the bug
	}

	// start := time.Now()
	var wg sync.WaitGroup

	v := &ChanVisitor{}
	v.Ch = make(chan interface{}, 1000)
	first := true
	wg.Add(1)
	var appendMutex sync.Mutex
	go func() {
		i := 0
		for t := range v.Ch {
			// doing this because s.Walk can finish, before reaching here
			if first {
				first = false
			} else {
				wg.Add(1)
			}
			x := t.(*Association)

			go func(i int, x *Association) {
				defer wg.Done()

				if i >= int(size) { //In case results change since size was calculated
					appendMutex.Lock()
					for j := len(set.Keys); j <= i; j++ {
						set.Keys = append(set.Keys, "")
						set.Associations = append(set.Associations, "")
						set.Relations = append(set.Relations, "")
						associationTrees = append(associationTrees, &Tree{})
					}
					appendMutex.Unlock()
				}
				set.Keys[i] = ic.GetValueString(x.Level, x.EntryId)
				set.Associations[i] = ic.GetValueString(x.AssociationLevel, x.AssociateTo)
				associationTrees[i] = ic.GetAssociationsFromEntry(ic.GetEntry(x.AssociationLevel, x.AssociateTo))
				// fmt.Println("Associate to:", x.AssociationLevel, x.AssociateTo, ic.GetValueString(x.AssociationLevel, x.AssociateTo))
				set.Relations[i] = ic.GetValueString(x.RelationLevel, x.Relation)
			}(i, x)
			i++
		}
	}()
	s.Walk(v)
	wg.Wait()
	close(v.Ch)

	//Filters - probably should be done differently

	if relationFilter != "" {
		n := 0

		for i, x := range set.Relations {
			if x == relationFilter {
				set.Relations[n] = x
				set.Associations[n] = set.Associations[i]
				associationTrees[n] = associationTrees[i]
				n++
			}
		}
		set.Associations = set.Associations[:n]
		set.Relations = set.Relations[:n]
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
