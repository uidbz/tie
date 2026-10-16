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
// to the process slog logger (which tie-triplestore/tie-filehost point at a log file
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
			ic.bumpTotalEntries(entryID)
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

// dbFile is the record-file handle the writer goroutine uses. It is an
// interface so tests can inject I/O faults (ENOSPC, short writes, a dead
// device) without mounting a tiny filesystem.
type dbFile interface {
	io.WriterAt
	io.ReaderAt
	Sync() error
	Close() error
}

// openDB opens (creating if needed) the DB file handle. It does NOT set db_size:
// db_size is the append cursor owned by allocSlot and initialized once by
// initDBSize. A lazy reopen must not reset it, or an allocSlot bump made while
// the file was idle-closed would be clobbered and hand out a colliding offset.
func (t *Collection) openDB() (dbFile, error) {
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening collection file %s: %w", t.dBFullPath, err)
	}
	return f, nil
}

// initDBSize establishes db_size (the end-of-file append cursor) exactly once,
// before the writer goroutine starts and before any Add can call allocSlot. A
// crash mid-write can leave a trailing partial record; that tail was never
// acknowledged, so trim it back to a record boundary rather than refusing to
// open the whole DB.
func (t *Collection) initDBSize() error {
	fi, err := os.Stat(t.dBFullPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.db_size = 0
			return nil
		}
		return fmt.Errorf("stat %s: %w", t.dBFullPath, err)
	}
	t.db_size = fi.Size()
	rem := t.db_size % ENTRY_SIZE
	if rem == 0 {
		return nil
	}
	trimmed := t.db_size - rem
	slog.Warn("partial trailing record; truncating",
		"path", t.dBFullPath, "partialBytes", rem, "trimTo", trimmed)
	f, err := os.OpenFile(t.dBFullPath, os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening %s for truncation: %w", t.dBFullPath, err)
	}
	if err := f.Truncate(trimmed); err != nil {
		f.Close()
		return fmt.Errorf("truncating partial record in %s: %w", t.dBFullPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s after truncation: %w", t.dBFullPath, err)
	}
	t.db_size = trimmed
	return nil
}

// applyFileMod persists one queued modification to the open db handle. It runs
// only on the writer goroutine, which owns the fd, so it takes no lock.
//
// An I/O error (ENOSPC, a dying device) is RETURNED, not panicked: the writer
// records it as the collection's sticky error, keeps serving reads, and every
// later write reports it. Panicking here killed the whole triplestore — every
// other collection and every in-flight request — because one collection's disk
// was full, and lost the acknowledged writes still queued. Only programmer
// errors (an impossible mode or entry type) still panic.
func (ic *Collection) applyFileMod(db dbFile, file_mod FileMod) error {
	var n int
	var err error

	switch file_mod.Mode {
	case FILE_BARRIER:
		// The queue is FIFO, so everything queued before the barrier has
		// already been applied: this is the durability point. fsync, then
		// release the caller. No record is written, so return before the
		// short-write check.
		if file_mod.Done != nil {
			if err := db.Sync(); err != nil {
				slog.Error("barrier DB sync error", "err", err)
			}
			close(file_mod.Done)
		}
		return nil

	case FILE_DELETE:
		// Every association in the index carries a real slot offset (slots are
		// reserved before the index insert), so there is no placeholder case.
		pos := file_mod.Position
		b := make([]byte, ENTRY_SIZE)
		binary.LittleEndian.PutUint16(b, TYPE_DELETE)
		n, err = db.WriteAt(b, pos)
		// Free only after the tombstone is on the file (freeSlot's contract): a
		// crash between the two must not leave a reusable slot whose old record
		// is still live on disk.
		if err == nil && n == ENTRY_SIZE {
			ic.freeSlot(pos)
		}

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
		return fmt.Errorf("writing record at %d: %w", file_mod.Position, err)
	}
	if n != ENTRY_SIZE {
		return fmt.Errorf("short write at %d: wrote %d bytes, expected %d", file_mod.Position, n, ENTRY_SIZE)
	}
	return nil
}

// drainWriteQueue applies every write currently queued, without blocking. The
// writer goroutine calls it before serving a read (read-your-writes) and on
// shutdown so no acknowledged Add is lost.
func (ic *Collection) drainWriteQueue(db dbFile) {
	for {
		select {
		case file_mod := <-ic.dBWriteQueue:
			ic.applyFileMod(db, file_mod)
		default:
			return
		}
	}
}

// readSlot reads and decodes the record at pos, guaranteeing read-your-writes.
// Collection.Add reserves a slot with allocSlot, queues the FILE_ADD, and only
// then inserts the association into the in-memory index (making it visible to
// queries). A reader can therefore only ask for a slot whose write is already
// in the queue; draining the queue first puts the bytes on disk before the
// read. The old index-before-enqueue order left a window where a reclaimed
// slot read back its old tombstone, so readSlot used to spin (up to a second)
// waiting for the write to arrive; with the queue-first order that window is
// gone and the spin is deleted.
func (ic *Collection) readSlot(db dbFile, pos int64) (Triple, bool) {
	b := make([]byte, ENTRY_SIZE)
	ic.drainWriteQueue(db)
	n, err := db.ReadAt(b, pos)
	if err != nil {
		ic.freespaceMutex.Lock()
		allocated := pos >= 0 && pos < ic.db_size
		ic.freespaceMutex.Unlock()
		slog.Error("read error", "err", err, "bytesRead", n, "expected", ENTRY_SIZE, "position", pos, "allocated", allocated)
		return Triple{}, false
	}
	a, decodeErr := ic.bufToAssociation([ENTRY_SIZE]byte(b))
	if decodeErr != nil {
		slog.Error("decode error", "position", pos, "err", decodeErr)
		return Triple{}, false
	}
	return a, true
}
func (ic *Collection) dBWriter() {
	// Queues absorb write bursts; when full, Add blocks — back-pressure is
	// the point. 100 000 slots per collection (~a few MB of channel buffer)
	// let a caller loop mint unbounded pending work; 4096 is plenty for
	// burst smoothing and bounds memory per collection.
	ic.dBWriteQueue = make(chan FileMod, 4096)
	ic.dBReadQueue = make(chan ReadRequest, 4096)
	ic.dBCloseWriter = make(chan bool, 1)

	// db_size is the append cursor consumed by allocSlot. Set it once here,
	// before any Add can allocate.
	ic.writerDone = make(chan struct{})

	if err := ic.initDBSize(); err != nil {
		ic.setIOErr(err)
		slog.Error("collection unavailable: cannot size the record file", "path", ic.dBFullPath, "err", err)
		close(ic.writerDone)
		return
	}

	// The writer goroutine owns the fd for its whole lifetime: it opens once
	// here and closes only on shutdown. Durability comes from a periodic Sync on
	// syncInterval rather than from closing the handle when idle (holding an fd
	// open is free; the old idle-close existed only to force that Sync).
	db, err := ic.openDB()
	if err != nil {
		ic.setIOErr(err)
		slog.Error("collection unavailable: cannot open the record file", "path", ic.dBFullPath, "err", err)
		close(ic.writerDone)
		return
	}
	ic.setFile(db)
	syncInterval := time.Second * 10

	// finished lets closeDB block until the fd is synced and closed.
	ic.finished.Add(1)
	go func() {
		defer close(ic.writerDone)
		defer ic.finished.Done()
		syncTicker := time.NewTicker(syncInterval)
		defer syncTicker.Stop()

		for {
			select {
			case req := <-ic.dBReadQueue:
				a, ok := ic.readSlot(ic.getFile(), req.Position)
				if ok {
					req.ReplyChan <- a
				} else {
					close(req.ReplyChan) // signal failure to readTripleAt
				}

			case file_mod := <-ic.dBWriteQueue:
				if werr := ic.applyFileMod(ic.getFile(), file_mod); werr != nil {
					// Sticky: reads keep working, every later write reports it.
					ic.setIOErr(werr)
					slog.Error("collection write failed; making it read-only",
						"path", ic.dBFullPath, "err", werr)
				}

			case <-syncTicker.C:
				if err := ic.getFile().Sync(); err != nil {
					slog.Error("periodic DB sync error", "err", err)
				}

			case <-ic.dBCloseWriter:
				// Drain any writes already queued so no acknowledged Add is lost,
				// then sync and close. Reads are best-effort and can be dropped.
				ic.drainWriteQueue(ic.getFile())
				if err := ic.getFile().Sync(); err != nil {
					slog.Error("DB sync error on close", "err", err)
				}
				if err := ic.getFile().Close(); err != nil {
					slog.Error("DB close error on close", "err", err)
				}
				return
			}
		}
	}()
}
