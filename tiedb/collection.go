package tiedb

// SetReverseRelations restricts which relations (value1) this collection indexes
// in reverse. Pass nil to index every relation (the default). Call before adding
// triples; it does not rebuild reverse indexes for triples already inserted.
func (ic *Collection) SetReverseRelations(relations []string) {
	if relations == nil {
		ic.reverseRelations = nil
		return
	}
	set := make(map[string]bool, len(relations))
	for _, r := range relations {
		set[r] = true
	}
	ic.reverseRelations = set
}

// SetBlobPolicy installs a whole-value blob policy on this collection and
// allocates the backing hash store. Pass nil to keep the trie-only behavior. See
// [[BlobPolicy]]. Call before values are inserted.
func (ic *Collection) SetBlobPolicy(p *BlobPolicy) {
	ic.blobPolicy = p
	if p == nil {
		ic.hashEntries = nil
		ic.hashValues = nil
		return
	}
	ic.hashEntries = newLockedTree[uint64, [32]byte](uint64Hash)
	ic.hashValues = newLockedTree[[32]byte, uint64](hashHash)
	ic.hashAssociations = newLockedTree[uint64, *AssociationSet](uint64Hash)
	ic.hashReverseAssociations = newLockedTree[uint64, *AssociationSet](uint64Hash)
}

// associationsTree returns the forward-association tree housing associations
// keyed by an entry at level: the per-level tree, or the dedicated hash tree for
// HASH_LEVEL entries. The bool is false when no such tree exists.
func (ic *Collection) associationsTree(level int) (*lockedTree[uint64, *AssociationSet], bool) {
	if level == HASH_LEVEL {
		return ic.hashAssociations, ic.hashAssociations != nil
	}
	if lvl, ok := ic.levelAt(level); ok {
		return lvl.associations, true
	}
	return nil, false
}

// reverseAssociationsTree is the reverse-index counterpart of associationsTree.
func (ic *Collection) reverseAssociationsTree(level int) (*lockedTree[uint64, *AssociationSet], bool) {
	if level == HASH_LEVEL {
		return ic.hashReverseAssociations, ic.hashReverseAssociations != nil
	}
	if lvl, ok := ic.levelAt(level); ok {
		return lvl.reverseAssociations, true
	}
	return nil, false
}

// Sync is an explicit durability request: it blocks until every write queued
// before this call has been applied to the collection file and the file has
// been fsynced. It does NOT wait for other callers' writes — the old
// WaitGroup-based Sync did, which both starved under a steady import from
// another client and misused the WaitGroup (Add with a positive delta
// concurrent with Wait can panic).
//
// Callers do not need Sync for correctness: the in-memory index is updated
// synchronously and readSlot drains the queue, so reads are already
// read-your-writes. Sync only bounds what a crash can lose.
func (ic *Collection) Sync() {
	if !ic.writeToDisk || ic.closed.Load() {
		return
	}
	ch := make(chan struct{})
	if !ic.queueWrite(FileMod{Mode: FILE_BARRIER, Done: ch}) {
		return // writer gone: it drained and synced on the way out
	}
	select {
	case <-ch:
	case <-ic.writerDone:
		// The writer shut down; it drained and synced on the way out.
	}
}

// Err reports the sticky I/O failure that put this collection read-only, or
// nil while it is healthy. A failed write (ENOSPC, a dying device) does not
// kill the process and does not corrupt the other collections: the writer
// records the error, keeps serving reads, and every later write reports it.
func (ic *Collection) Err() error {
	ic.ioErrMutex.Lock()
	defer ic.ioErrMutex.Unlock()
	return ic.ioErr
}

// setIOErr records the first I/O failure.
func (ic *Collection) setIOErr(err error) {
	if err == nil {
		return
	}
	ic.ioErrMutex.Lock()
	defer ic.ioErrMutex.Unlock()
	if ic.ioErr == nil {
		ic.ioErr = err
	}
}

func (ic *Collection) closeDB() {
	ic.closed.Store(true)
	// Signal the writer to drain any queued writes, sync, and close the fd, then
	// wait for it to exit (it calls finished.Done() on return). Do NOT close
	// dBWriteQueue: nothing ranges over it, and a closed channel would make the
	// writer's select case perpetually ready with a zero-value FileMod, racing
	// the shutdown signal into the "Wrong EntryType" panic.
	ic.dBCloseWriter <- true
	ic.finished.Wait()
}

// setFile/getFile give the writer goroutine (and tests injecting I/O faults)
// race-free access to the collection's record handle. The holder wrapper keeps
// atomic.Value's concrete type constant across swaps.
type fileHolder struct{ f dbFile }

func (ic *Collection) setFile(f dbFile) { ic.file.Store(fileHolder{f}) }

func (ic *Collection) getFile() dbFile {
	v := ic.file.Load()
	if v == nil {
		return nil
	}
	return v.(fileHolder).f
}
