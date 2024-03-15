package tiedb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

func (ic *Collection) bufToEntry(buf []byte) (Entry, int) {
	var last int = SIZE_DATATYPE
	var e Entry

	next := last + SIZE_LEVEL
	level := int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	e.Id = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	e.UniqueValue = &UniqueValue{}

	next = last + SIZE_PARENTID
	e.UniqueValue.ParentId = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_VALUE
	e.UniqueValue.Value = ([SIZE_VALUE]byte)(buf[last:next])
	last = next

	return e, level
}

func (ic *Collection) bufToAssociation(buf []byte) Triple {
	var a Triple
	var last int = SIZE_DATATYPE

	next := last + SIZE_LEVEL
	a.Level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.Key = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.Value2Level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.Value2 = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_ID
	a.Value1Level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	a.Value1 = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	return a
}

func (ic *Collection) inserterWorker(wg *sync.WaitGroup) {
	for entry := range ic.rawDataToLoad {
		switch int(binary.LittleEndian.Uint16(entry.Data[:SIZE_DATATYPE])) {
		case TYPE_ENTRY:
			e, level := ic.bufToEntry(entry.Data)
			ic.totalEntriesMutex.Lock()
			if e.Id > ic.totalEntries {
				ic.totalEntries = e.Id
			}
			ic.totalEntriesMutex.Unlock()
			ic.insertEntry(level, &e)

		case TYPE_ASSOCIATION:
			a := ic.bufToAssociation(entry.Data)
			ic.insertAssociation(a.Level, &a, entry.Position)

		case TYPE_DELETE:
			if len(ic.freespace) < MaxFreespace {
				ic.freespace <- entry.Position
			}
		}
	}
	wg.Done()
}

func (t *Collection) loadDB(filename string) error {
	fi, err := os.Stat(filename)
	if err != nil {
		return err
	}
	dbname := "(" + fi.Name() + ") "
	fmt.Println(dbname+"DB size:", (fi.Size() / 1024), "KiB")

	db, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer db.Close()

	Info(dbname + "Loading DB...")
	t.rawDataToLoad = make(chan RawDataEntry, 100000)

	// Start inserter workers
	workers := 1
	workersFinished := sync.WaitGroup{}
	for i := 0; i < workers; i++ {
		workersFinished.Add(1)
		go t.inserterWorker(&workersFinished)
	}

	var entry int64
	for {
		buf := make([]byte, ENTRY_SIZE*4194304)
		v, err := db.Read(buf)
		if err != nil && !errors.Is(err, io.EOF) {
			panic("Error reading database: " + err.Error())
		}
		if v == 0 {
			break
		}

		for i := 0; i < v; i = i + ENTRY_SIZE {
			r := RawDataEntry{
				Position: entry * ENTRY_SIZE,
				Data:     buf[i : i+ENTRY_SIZE],
			}
			t.rawDataToLoad <- r
			entry++
		}
	}
	close(t.rawDataToLoad)
	workersFinished.Wait()
	Info(dbname + "Loading DB: Done!")
	return nil
}

func (t *Collection) openDB() *os.File {
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR|os.O_CREATE, 0600)
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

func (ic *Collection) getAssociationPosition(level int, a *Triple) (pos int64) {
	pos = -1

	if s, found := ic.levels[level].associations.Get(a.Key); found {
		ass := s.(*TieTree)
		key := UniqueAssociation{
			AssociateTo: a.Value2,
			Relation:    a.Value1,
		}
		if v, f := ass.Get(key); f {
			pos = v.(int64)
		}
	}

	return pos
}

func (ic *Collection) dBWriter() {
	ic.dBWriteQueue = make(chan FileMod, 100000)
	ic.dBReadQueue = make(chan ReadRequest, 100000)
	ic.dBCloseWriter = make(chan bool, 1)

	var db *os.File
	var open bool = false
	var timeout *time.Timer
	var openLock sync.Mutex

	closeAfter := time.Second * 10

	openDb := func() {
		if !open {
			db = ic.openDB()
			open = true
			ic.finished.Add(1)
			timeout = time.AfterFunc(closeAfter, func() {
				openLock.Lock()
				open = false
				db.Sync()
				db.Close()
				openLock.Unlock()
				ic.finished.Done()
			})
		} else {
			timeout.Reset(time.Second * 10)
			open = true
		}
	}
	go func() {
		for {
			select {
			case req := <-ic.dBReadQueue:
				openLock.Lock()
				openDb()
				b := make([]byte, ENTRY_SIZE)
				n, err := db.ReadAt(b, req.Position)
				if err != nil {
					log.Println("Read error:", err, "bytes read,", n, "expected", ENTRY_SIZE)
				} else {
					req.ReplyChan <- ic.bufToAssociation(b)
				}
				ic.dbReadWg.Done()
				openLock.Unlock()

			case file_mod := <-ic.dBWriteQueue:
				openLock.Lock()
				openDb()
				var n int
				var err error
				var pos int64

				switch file_mod.Mode {
				case FILE_DELETE:
					b := make([]byte, ENTRY_SIZE)
					datatype := make([]byte, SIZE_DATATYPE)
					binary.LittleEndian.PutUint16(datatype, TYPE_DELETE)
					b = append(datatype, b[2:]...)
					pos = file_mod.Position
					if pos != -1 {
						if len(ic.freespace) < MaxFreespace {
							ic.freespace <- pos
						}
						n, err = db.WriteAt(b, pos)
					}

				case FILE_ADD:
					if len(ic.freespace) == 0 {
						pos = ic.db_size
						ic.db_size += ENTRY_SIZE
					} else {
						pos = <-ic.freespace
					}
					switch file_mod.EntryType {
					case TYPE_ASSOCIATION:
						ic.insertAssociation(file_mod.Level, file_mod.Association, pos)
						n, err = db.WriteAt(file_mod.Association.toBytes(), pos)
						ic.finishedAdding.Done()
					case TYPE_ENTRY:
						n, err = db.WriteAt(file_mod.Entry.toBytes(file_mod.Level), pos)
					default:
						panic("Wrong EntryType provided for DB writer.")
					}

				default:
					panic("Wrong 'Mode' provided for DB writer" + strconv.Itoa(file_mod.Mode))
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
