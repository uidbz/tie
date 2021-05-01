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

func (ic *InternalCollection) BufToEntry(buf []byte, pos int64) Entry {
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
	found, valPtr := ic.Values.Get(value)
	if !found {
		ic.Values.Put(value, &value)
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
func (ic *InternalCollection) BufToAssociation(buf []byte, pos int64) Association {
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

func (ic *InternalCollection) BufToAssociationExt(buf []byte, pos int64) AssociationExt {
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

func (ic *InternalCollection) InsertAllEntries() {
	ic.wg.Add(1)
	var i int64 = 0

	go func() {
		for buf := range ic.data {
			pos := i * ENTRY_SIZE
			switch int(binary.LittleEndian.Uint16(buf[:SIZE_DATATYPE])) {
			case TYPE_ENTRY:
				e := ic.BufToEntry(buf, pos)
				if e.Id > ic.TotalEntries {
					ic.TotalEntries = e.Id
				}
				ic.InsertEntry(e.Level, &e)

			case TYPE_ASSOCIATION:
				a := ic.BufToAssociation(buf, pos)
				ic.InsertAssociation(a.Level, &a)

			case TYPE_ASSOCIATIONEXT:
				aExt := ic.BufToAssociationExt(buf, pos)
				ic.InsertAssociationExt(aExt.AssociationCollectionLevel, &aExt)

			case TYPE_DELETE:
				e := ic.BufToEntry(buf, pos)
				if e.Id > ic.TotalEntries {
					ic.TotalEntries = e.Id
				}
				if len(ic.Freespace) < MaxFreespace {
					ic.Freespace <- &e
				}
			}

			i = i + 1
		}
		ic.wg.Done()
	}()
}

func OpenDBRead(filename string) (bool, *os.File) {
	fi, err := os.Stat(filename)

	if err == nil {
		fmt.Println("DB size:", fi.Size())
	}
	db, err := os.Open(filename)

	if err != nil {
		fmt.Println("Error opening DB file:", filename)
		fmt.Println(err.Error())
		return false, nil
	}

	return true, db
}

func (t *InternalCollection) LoadDB(db *os.File) {
	defer db.Close()

	Info("Loading DB")
	t.data = make(chan []byte, ENTRY_SIZE)

	// Start inserter worker
	t.InsertAllEntries()

	for {
		buf := make([]byte, ENTRY_SIZE)
		v, _ := db.Read(buf)

		if v == 0 {
			break
		}
		t.data <- buf // feed channel with input
	}
	close(t.data)
	t.wg.Wait()
	t.Sync()
	Info("Done reading index")
}

func (t *InternalCollection) OpenDBWrite() *os.File {
	f, err := os.OpenFile(t.DBFullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	fi, _ := os.Stat(t.DBFullPath)
	t.db_size = fi.Size()
	if t.db_size%ENTRY_SIZE != 0 {
		panic("Assertion: db_size % ENTRY_SIZE != 0. DB probably corrupt.")
	}
	return f
}

func (t *InternalCollection) OpenDBUpdate() *os.File {
	f, err := os.OpenFile(t.DBFullPath, os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	fi, _ := os.Stat(t.DBFullPath)
	t.db_size = fi.Size()
	if t.db_size%ENTRY_SIZE != 0 {
		panic("Assertion: db_size % ENTRY_SIZE != 0. DB probably corrupt.")
	}
	return f
}

func (ic *InternalCollection) DBWriter() {
	ic.DBWriteQueue = make(chan FileMod, 1000)
	ic.DBCloseWriter = make(chan bool, 1)
	var db *os.File
	var open bool = false
	var mode_append bool = true
	var timeout *time.Timer
	var openLock sync.Mutex

	go func() {
		for {
			select {
			case file_mod := <-ic.DBWriteQueue:
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
						db = ic.OpenDBWrite()
					} else {
						db = ic.OpenDBUpdate()
					}
					open = true
					ic.Finished.Add(1)
					timeout = time.AfterFunc(time.Second*10, func() {
						openLock.Lock()
						open = false
						db.Close()
						openLock.Unlock()
						ic.Finished.Done()
					})
				} else {
					timeout.Reset(time.Second * 10)
					open = true
				}
				var n int
				var err error
				if mode_append {
					file_mod.Entry.SetPosition(ic.db_size)
					n, err = db.Write(file_mod.Entry.ToBytes())
					ic.db_size += ENTRY_SIZE
				} else {
					b := file_mod.Entry.ToBytes()
					pos := file_mod.Entry.GetPosition()
					if file_mod.Mode == FILE_DELETE {
						datatype := make([]byte, SIZE_DATATYPE)
						binary.LittleEndian.PutUint16(datatype, TYPE_DELETE)
						b = append(datatype, b[2:]...)
					}
					n, err = db.WriteAt(b, pos)
					if file_mod.Mode == FILE_DELETE {
						if len(ic.Freespace) < MaxFreespace {
							ic.Freespace <- file_mod.Entry
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

			case closewriter := <-ic.DBCloseWriter:
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
