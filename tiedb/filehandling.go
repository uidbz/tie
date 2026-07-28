package tiedb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// Debug and Info are thin wrappers kept for existing call sites; they forward
// to the process slog logger (which tie-daemon/tie-filehost point at a log file
// plus pretty stderr). Diagnostics stay off stdout — stdout is data.
func Debug(value string) { slog.Debug(value) }

func Info(value string) { slog.Info(value) }

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

// bufToHash decodes a TYPE_HASH record: the entry ID followed by the 32 raw
// hash bytes that occupy the frame's parentId+value region. The level field is
// always HASH_LEVEL and is not read back.
func (ic *Collection) bufToHash(buf [ENTRY_SIZE]byte) (entryID uint64, raw [32]byte) {
	last := SIZE_DATATYPE + SIZE_LEVEL
	next := last + SIZE_ID
	entryID = binary.LittleEndian.Uint64(buf[last:next])
	raw = [32]byte(buf[next : next+32])
	return entryID, raw
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

		case TYPE_HASH:
			entryID, raw := ic.bufToHash(entry.Data)
			ic.loadHash(entryID, raw)

		case TYPE_DELETE:
			ic.freeSlot(entry.Position)
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
				slog.Warn("skipping corrupt association", "position", entry.Position, "err", err)
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
	slog.Info("DB size", "db", fi.Name(), "kib", fi.Size()/1024)

	db, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer db.Close()

	Info(dbname + "Loading DB...")

	// A bulk load builds a large, almost-entirely-retained heap, so the default
	// GC pace spends cycles re-scanning a live set that never shrinks. Relax the
	// pacer for the duration of the load (trading transient peak RAM for far
	// fewer collections) and restore it afterwards.
	prevGC := debug.SetGCPercent(200)
	defer debug.SetGCPercent(prevGC)

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
	rawDataToLoad := make(chan RawDataEntry, 8192)

	workers := 8
	workersFinished := sync.WaitGroup{}
	workersFinished.Add(workers)
	for range workers {
		go worker(rawDataToLoad, &workersFinished)
	}

	// One reusable read buffer for the whole pass. Reallocating it per iteration
	// churned ~16 GiB of garbage on a large DB and dominated GC time at load.
	buf := make([]byte, ENTRY_SIZE*1024*1024)
	var entry int64
	for {
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

// openDB opens (creating if needed) the DB file handle. It does NOT set db_size:
// db_size is the append cursor owned by allocSlot and initialized once by
// initDBSize. A lazy reopen must not reset it, or an allocSlot bump made while
// the file was idle-closed would be clobbered and hand out a colliding offset.
func (t *Collection) openDB() *os.File {
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		panic(err)
	}
	return f
}

// initDBSize establishes db_size (the end-of-file append cursor) exactly once,
// before the writer goroutine starts and before any Add can call allocSlot. A
// crash mid-write can leave a trailing partial record; that tail was never
// acknowledged, so trim it back to a record boundary rather than refusing to
// open the whole DB.
func (t *Collection) initDBSize() {
	fi, err := os.Stat(t.dBFullPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.db_size = 0
			return
		}
		panic(err)
	}
	t.db_size = fi.Size()
	rem := t.db_size % ENTRY_SIZE
	if rem == 0 {
		return
	}
	trimmed := t.db_size - rem
	slog.Warn("partial trailing record; truncating",
		"path", t.dBFullPath, "partialBytes", rem, "trimTo", trimmed)
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR, 0600)
	if err != nil {
		panic("tiedb: failed to open for truncation: " + err.Error())
	}
	if err := f.Truncate(trimmed); err != nil {
		panic("tiedb: failed to truncate partial record: " + err.Error())
	}
	if err := f.Close(); err != nil {
		panic("tiedb: failed to close after truncation: " + err.Error())
	}
	t.db_size = trimmed
}

// applyFileMod persists one queued modification to the open db handle. It runs
// only on the writer goroutine, which owns the fd, so it takes no lock. A
// FILE_DELETE with Position -1 (the tree placeholder for a triple whose FILE_ADD
// has not yet been processed) has no on-disk record to tombstone, so it is
// skipped. Any I/O error or short write is fatal — the DB is presumed corrupt.
func (ic *Collection) applyFileMod(db *os.File, file_mod FileMod) {
	var n int
	var err error

	switch file_mod.Mode {
	case FILE_DELETE:
		pos := file_mod.Position
		if pos == -1 {
			return
		}
		b := make([]byte, ENTRY_SIZE)
		binary.LittleEndian.PutUint16(b, TYPE_DELETE)
		ic.freeSlot(pos)
		n, err = db.WriteAt(b, pos)

	case FILE_ADD:
		// The producer reserved the offset via allocSlot and already inserted the
		// association into the tree at that position, so the writer only persists
		// the bytes — it never re-inserts. (Re-inserting here raced a concurrent
		// Delete: the delete could remove the placeholder entry before this ran,
		// and the re-insert would silently resurrect the deleted triple.)
		pos := file_mod.Position
		switch file_mod.EntryType {
		case TYPE_ASSOCIATION:
			n, err = db.WriteAt(file_mod.Triple.toBytes(), pos)
			ic.finishedAdding.Done()
		case TYPE_ENTRY:
			n, err = db.WriteAt(EntryToBytes(file_mod.Level, file_mod.EntryID, file_mod.UniqueValue), pos)
		case TYPE_HASH:
			n, err = db.WriteAt(HashToBytes(file_mod.EntryID, file_mod.HashValue), pos)
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
		panic(fmt.Sprintf("Assertion: wrote %d bytes, expected %d. DB probably corrupt.", n, ENTRY_SIZE))
	}
}

func (ic *Collection) dBWriter() {
	ic.dBWriteQueue = make(chan FileMod, 100000)
	ic.dBReadQueue = make(chan ReadRequest, 100000)
	ic.dBCloseWriter = make(chan bool, 1)

	// db_size is the append cursor consumed by allocSlot. Set it once here,
	// before any Add can allocate.
	ic.initDBSize()

	// The writer goroutine owns the fd for its whole lifetime: it opens once
	// here and closes only on shutdown. Durability comes from a periodic Sync on
	// syncInterval rather than from closing the handle when idle (holding an fd
	// open is free; the old idle-close existed only to force that Sync).
	db := ic.openDB()
	syncInterval := time.Second * 10

	// finished lets closeDB block until the fd is synced and closed.
	ic.finished.Add(1)
	go func() {
		defer ic.finished.Done()
		syncTicker := time.NewTicker(syncInterval)
		defer syncTicker.Stop()

		for {
			select {
			case req := <-ic.dBReadQueue:
				b := make([]byte, ENTRY_SIZE)
				n, err := db.ReadAt(b, req.Position)
				if err != nil {
					slog.Error("read error", "err", err, "bytesRead", n, "expected", ENTRY_SIZE)
					close(req.ReplyChan) // signal failure to readTripleAt
				} else {
					a, decodeErr := ic.bufToAssociation([ENTRY_SIZE]byte(b))
					if decodeErr != nil {
						slog.Error("decode error", "position", req.Position, "err", decodeErr)
						close(req.ReplyChan)
					} else {
						req.ReplyChan <- a
					}
				}

			case file_mod := <-ic.dBWriteQueue:
				ic.applyFileMod(db, file_mod)

			case <-syncTicker.C:
				if err := db.Sync(); err != nil {
					slog.Error("periodic DB sync error", "err", err)
				}

			case <-ic.dBCloseWriter:
				// Drain any writes already queued so no acknowledged Add is lost,
				// then sync and close. Reads are best-effort and can be dropped.
				for drained := false; !drained; {
					select {
					case file_mod := <-ic.dBWriteQueue:
						ic.applyFileMod(db, file_mod)
					default:
						drained = true
					}
				}
				if err := db.Sync(); err != nil {
					slog.Error("DB sync error on close", "err", err)
				}
				if err := db.Close(); err != nil {
					slog.Error("DB close error on close", "err", err)
				}
				return
			}
		}
	}()
}
