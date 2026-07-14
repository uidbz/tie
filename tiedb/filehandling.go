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

func (ic *Collection) bufToEntry(buf [ENTRY_SIZE]byte) (level int, entryID uint64, uv *UniqueValue) {
	var last int = SIZE_DATATYPE

	next := last + SIZE_LEVEL
	level = int(binary.LittleEndian.Uint64(buf[last:next]))
	last = next

	next = last + SIZE_ID
	entryID = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	uv = &UniqueValue{}

	next = last + SIZE_PARENTID
	uv.ParentId = binary.LittleEndian.Uint64(buf[last:next])
	last = next

	next = last + SIZE_VALUE
	uv.Value = ([SIZE_VALUE]byte)(buf[last:next])
	last = next

	return level, entryID, uv
}

func (ic *Collection) bufToAssociation(buf [ENTRY_SIZE]byte) (Triple, error) {
	var a Triple
	if dt := binary.LittleEndian.Uint16(buf[:SIZE_DATATYPE]); dt != TYPE_ASSOCIATION {
		return a, fmt.Errorf("expected association record (type %d), got type %d", TYPE_ASSOCIATION, dt)
	}
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

	return a, nil
}

// loadEntries is pass 1 of the two-pass load: it inserts trie entries (and
// reclaims freed slots) but skips associations. Associations must wait until
// every entry exists, because shouldBuildReverse resolves a relation's string
// by walking the entries trie, and loadAssociations runs workers concurrently
// in arbitrary order.
func (ic *Collection) loadEntries(rawDataToLoad chan RawDataEntry, wg *sync.WaitGroup) {
	for entry := range rawDataToLoad {
		switch int(binary.LittleEndian.Uint16(entry.Data[:SIZE_DATATYPE])) {
		case TYPE_ENTRY:
			level, entryID, uv := ic.bufToEntry(entry.Data)
			ic.totalEntriesMutex.Lock()
			if entryID > ic.totalEntries {
				ic.totalEntries = entryID
			}
			ic.totalEntriesMutex.Unlock()
			ic.insertEntry(level, entryID, uv)

		case TYPE_DELETE:
			if len(ic.freespace) < MaxFreespace {
				ic.freespace <- entry.Position
			}
		}
	}
	wg.Done()
}

// loadAssociations is pass 2 of the two-pass load: it inserts associations once
// all entries from pass 1 are present.
func (ic *Collection) loadAssociations(rawDataToLoad chan RawDataEntry, wg *sync.WaitGroup) {
	for entry := range rawDataToLoad {
		if int(binary.LittleEndian.Uint16(entry.Data[:SIZE_DATATYPE])) == TYPE_ASSOCIATION {
			a, err := ic.bufToAssociation(entry.Data)
			if err != nil {
				log.Println("Skipping corrupt association at", entry.Position, ":", err)
				continue
			}
			ic.insertAssociation(a.Level, &a, entry.Position)
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

	// Two passes over the file: entries first, then associations. Associations
	// resolve their relation string against the entries trie (shouldBuildReverse),
	// so every entry must exist before any association is inserted.
	if err := t.loadPass(db, t.loadEntries); err != nil {
		return err
	}
	if _, err := db.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := t.loadPass(db, t.loadAssociations); err != nil {
		return err
	}

	Info(dbname + "Loading DB: Done!")
	return nil
}

// loadPass streams every record of the open db file through worker, which
// consumes a channel of RawDataEntry and decides which record types to handle.
func (t *Collection) loadPass(db *os.File, worker func(chan RawDataEntry, *sync.WaitGroup)) error {
	rawDataToLoad := make(chan RawDataEntry, 1024*1024*64)

	workers := 2
	workersFinished := sync.WaitGroup{}
	workersFinished.Add(workers)
	for i := 0; i < workers; i++ {
		go worker(rawDataToLoad, &workersFinished)
	}

	var entry int64
	for {
		buf := make([]byte, ENTRY_SIZE*1024*1024)
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
			}
			copy(r.Data[:], buf[i:i+ENTRY_SIZE])
			rawDataToLoad <- r
			entry++
		}
	}
	close(rawDataToLoad)
	workersFinished.Wait()
	return nil
}

func (t *Collection) openDB() *os.File {
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		panic(err)
	}
	fi, err := f.Stat()
	if err != nil {
		panic(err)
	}
	t.db_size = fi.Size()
	// A crash mid-write can leave a trailing partial record. That tail was never
	// acknowledged, so trim it back to a record boundary rather than refusing to
	// open the whole DB.
	if rem := t.db_size % ENTRY_SIZE; rem != 0 {
		trimmed := t.db_size - rem
		log.Printf("tiedb: %s has a %d-byte partial trailing record; truncating to %d bytes",
			t.dBFullPath, rem, trimmed)
		if err := f.Truncate(trimmed); err != nil {
			panic("tiedb: failed to truncate partial record: " + err.Error())
		}
		t.db_size = trimmed
	}
	return f
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
				if err := db.Sync(); err != nil {
					log.Println("tiedb: DB sync error on idle close:", err)
				}
				if err := db.Close(); err != nil {
					log.Println("tiedb: DB close error on idle close:", err)
				}
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
					close(req.ReplyChan) // signal failure to readTripleAt
				} else {
					a, decodeErr := ic.bufToAssociation([ENTRY_SIZE]byte(b))
					if decodeErr != nil {
						log.Println("Decode error at", req.Position, ":", decodeErr)
						close(req.ReplyChan)
					} else {
						req.ReplyChan <- a
					}
				}
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
						ic.insertAssociation(file_mod.Level, file_mod.Triple, pos)
						n, err = db.WriteAt(file_mod.Triple.toBytes(), pos)
						ic.finishedAdding.Done()
					case TYPE_ENTRY:
						n, err = db.WriteAt(EntryToBytes(file_mod.Level, file_mod.EntryID, file_mod.UniqueValue), pos)
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
					if open {
						open = false
						if err := db.Sync(); err != nil {
							log.Println("tiedb: DB sync error on close:", err)
						}
						if err := db.Close(); err != nil {
							log.Println("tiedb: DB close error on close:", err)
						}
					}
					fmt.Println("Closing DB writer")
					openLock.Unlock()
					return
				}
			}
		}
	}()
}
