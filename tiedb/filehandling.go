package tiedb

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"
)

// var data = make(chan []byte, ENTRY_SIZE)
// var wg sync.WaitGroup

func (ic *Collection) bufToEntry(buf []byte, pos int64) Entry {
	var last int = SIZE_DATATYPE
	var e Entry
	e.UniqueValue = UniqueValue{} // something is wrong here. Don't create new unique value all the time

	next := last + SIZE_LEVEL
	e.Level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	e.Id = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_PARENTID
	e.UniqueValue.ParentId = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_VALUE
	var value []byte
	value = append(value, buf[last:next]...)
	if valPtr, found := ic.values.Get(value); !found {
		ic.values.Put(value, &value)
		e.UniqueValue.Value = &value
	} else {
		e.UniqueValue.Value = valPtr.(*[]byte)
	}
	last = next

	e.Position = pos

	return e
}

// out = append(datatype[:], collection_level[:]...)
// out = append(out, entry_collection[:]...)
// out = append(out, entry_level[:]...)
// out = append(out, entry_id[:]...)
// out = append(out, association_collection_level[:]...)
// out = append(out, association_collection[:]...)
// out = append(out, association_level[:]...)
// out = append(out, association[:]...)
// out = append(out, relation_collection_level[:]...)
// out = append(out, relation_collection[:]...)
// out = append(out, relation_level[:]...)
// out = append(out, relation[:]...)

// out = append(datatype[:], entry_level[:]...)
// out = append(out, entry_id[:]...)
// out = append(out, association_level[:]...)
// out = append(out, association[:]...)
// out = append(out, relation_level[:]...)
// out = append(out, relation[:]...)
func (ic *Collection) bufToAssociation(buf []byte, pos int64) Association {
	var a Association
	var last int = SIZE_DATATYPE

	next := last + SIZE_LEVEL
	a.Level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.EntryId = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.AssociationLevel = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.AssociateTo = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.RelationLevel = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.Relation = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	a.Position = pos

	return a
}

// binary.LittleEndian.PutUint16(datatype, TYPE_ASSOCIATIONEXT)
// binary.LittleEndian.PutUint64(association, a.AssociateTo)
// binary.LittleEndian.PutUint64(relation, a.Relation)
// binary.LittleEndian.PutUint64(association_collection_level, uint64(a.AssociationCollectionLevel))
// binary.LittleEndian.PutUint64(association_collection, a.AssociationCollection)
// binary.LittleEndian.PutUint64(relation_collection_level, uint64(a.RelationCollectionLevel))
// binary.LittleEndian.PutUint64(relation_collection, a.RelationCollection)

func (ic *Collection) bufToAssociationExt(buf []byte, pos int64) AssociationExt {
	var a AssociationExt
	var last int = SIZE_DATATYPE

	next := last + SIZE_ID
	a.AssociateTo = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.Relation = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.AssociationCollectionLevel = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.AssociationCollection = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.RelationCollectionLevel = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.RelationCollection = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	a.Position = pos

	return a
}

func (ic *Collection) insertAllEntries() {
	ic.allDataLoaded.Add(1)
	var i int64 = 0

	go func() {
		for buf := range ic.rawDataToLoad {
			pos := i * ENTRY_SIZE
			switch int(binary.LittleEndian.Uint16(buf[:SIZE_DATATYPE])) {
			case TYPE_ENTRY:
				e := ic.bufToEntry(buf, pos)
				if e.Id > ic.totalEntries {
					ic.totalEntries = e.Id
				}
				ic.insertEntry(e.Level, &e)

			case TYPE_ASSOCIATION:
				a := ic.bufToAssociation(buf, pos)
				ic.insertAssociation(a.Level, &a)

			case TYPE_ASSOCIATIONEXT:
				aExt := ic.bufToAssociationExt(buf, pos)
				ic.insertAssociationExt(aExt.AssociationCollectionLevel, &aExt)

			case TYPE_DELETE:
				e := ic.bufToEntry(buf, pos)
				if e.Id > ic.totalEntries {
					ic.totalEntries = e.Id
				}
				if len(ic.freespace) < MaxFreespace {
					ic.freespace <- &e
				}
			}

			i = i + 1
		}
		ic.allDataLoaded.Done()
	}()
}

func (t *Collection) loadDB(filename string) error {
	fi, err := os.Stat(filename)
	if err != nil {
		return err
	}
	dbname := "(" + fi.Name() + ") "
	fmt.Println(dbname+"DB size:", (fi.Size() / 1024), "KB")

	db, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer db.Close()

	Info(dbname + "Loading DB...")
	t.rawDataToLoad = make(chan []byte, ENTRY_SIZE)

	// Start inserter worker
	t.insertAllEntries()

	for {
		buf := make([]byte, ENTRY_SIZE)
		v, _ := db.Read(buf)

		if v == 0 {
			break
		}
		t.rawDataToLoad <- buf // feed channel with input
	}
	close(t.rawDataToLoad)
	t.allDataLoaded.Wait()
	t.sync()
	Info(dbname + "Done reading index!")
	return nil
}

func (t *Collection) openDBWrite() *os.File {
	f, err := os.OpenFile(t.dBFullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	fi, _ := os.Stat(t.dBFullPath)
	t.db_size = fi.Size()
	if t.db_size%ENTRY_SIZE != 0 {
		panic("Assertion: db_size % ENTRY_SIZE != 0. DB probably corrupt.")
	}
	return f
}

func (t *Collection) openDBUpdate() *os.File {
	f, err := os.OpenFile(t.dBFullPath, os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	fi, _ := os.Stat(t.dBFullPath)
	t.db_size = fi.Size()
	if t.db_size%ENTRY_SIZE != 0 {
		panic("Assertion: db_size % ENTRY_SIZE != 0. DB probably corrupt.")
	}
	return f
}

func (ic *Collection) dBWriter() {
	ic.dBWriteQueue = make(chan FileMod, 1000)
	ic.dBCloseWriter = make(chan bool, 1)
	var db *os.File
	var open bool = false
	var mode_append bool = true
	var timeout *time.Timer
	var openLock sync.Mutex

	go func() {
		for {
			select {
			case file_mod := <-ic.dBWriteQueue:
				openLock.Lock()
				should_append := file_mod.Mode == FILE_APPEND
				if !mode_append && should_append {
					open = false
					db.Close()
					mode_append = true
				} else if mode_append && !should_append {
					open = false
					db.Close()
					mode_append = false
				}
				if !open {
					if should_append {
						db = ic.openDBWrite()
					} else {
						db = ic.openDBUpdate()
					}
					open = true
					ic.finished.Add(1)
					timeout = time.AfterFunc(time.Second*10, func() {
						openLock.Lock()
						open = false
						db.Close()
						openLock.Unlock()
						ic.finished.Done()
					})
				} else {
					timeout.Reset(time.Second * 10)
					open = true
				}
				var n int
				var err error
				if mode_append {
					file_mod.Entry.setPosition(ic.db_size)
					n, err = db.Write(file_mod.Entry.toBytes())
					ic.db_size += ENTRY_SIZE
				} else {
					b := file_mod.Entry.toBytes()
					pos := file_mod.Entry.getPosition()
					if file_mod.Mode == FILE_DELETE {
						datatype := make([]byte, SIZE_DATATYPE)
						binary.LittleEndian.PutUint16(datatype, TYPE_DELETE)
						b = append(datatype, b[2:]...)
					}
					n, err = db.WriteAt(b, pos)
					if file_mod.Mode == FILE_DELETE {
						if len(ic.freespace) < MaxFreespace {
							ic.freespace <- file_mod.Entry
						}
					}
				}

				if err != nil {
					panic(err)
				}

				if n != ENTRY_SIZE {
					fmt.Println("N", n)
					fmt.Println("Entry", ENTRY_SIZE)
					panic("Assertion: Bytes written differs from expected. DB probably corrupt.")
				}
				openLock.Unlock()

			case closewriter := <-ic.dBCloseWriter:
				if closewriter {
					openLock.Lock()
					open = false
					db.Sync()
					db.Close()
					fmt.Println("Closing DB writer")
					openLock.Unlock()
					return
				}
			}

		}

	}()
}
