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

func (ic *Collection) Sync() {
	ic.finishedAdding.Wait()
}

func (ic *Collection) closeDB() {
	// Signal the writer to drain any queued writes, sync, and close the fd, then
	// wait for it to exit (it calls finished.Done() on return). Do NOT close
	// dBWriteQueue: nothing ranges over it, and a closed channel would make the
	// writer's select case perpetually ready with a zero-value FileMod, racing
	// the shutdown signal into the "Wrong EntryType" panic.
	ic.dBCloseWriter <- true
	ic.finished.Wait()
}
