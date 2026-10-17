package tiedb

// Record slot allocation: the memory-mode arena and the disk-mode freespace
// stack / append cursor.

// arenaStore places a Triple in the memory-mode arena and returns its position,
// reusing a freed slot when one is available.
func (ic *Collection) arenaStore(a *Triple) int64 {
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	if n := len(ic.arenaFree); n > 0 {
		pos := ic.arenaFree[n-1]
		ic.arenaFree = ic.arenaFree[:n-1]
		ic.arena[pos] = *a
		return pos
	}
	ic.arena = append(ic.arena, *a)
	return int64(len(ic.arena) - 1)
}

// arenaLoad reads the Triple at a memory-mode arena position.
func (ic *Collection) arenaLoad(pos int64) (Triple, bool) {
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	if pos < 0 || pos >= int64(len(ic.arena)) {
		return Triple{}, false
	}
	return ic.arena[pos], true
}

// freeSlot records a freed disk slot offset for reuse by a later allocSlot. It
// must only be called by the writer goroutine after the slot's tombstone has
// been written, so a reused slot is never handed out before its old contents are
// overwritten on disk.
func (ic *Collection) freeSlot(pos int64) {
	if pos < 0 {
		return
	}
	ic.freespaceMutex.Lock()
	ic.freespace = append(ic.freespace, pos)
	ic.freespaceMutex.Unlock()
}

// allocSlot reserves the disk offset for the next record: a reclaimed freespace
// slot if one exists, otherwise a fresh slot appended at the end of the file.
// Both the freespace stack and db_size are guarded here, so concurrent Adds
// never hand out the same offset. Callers pass the returned offset to the writer
// as FileMod.Position.
func (ic *Collection) allocSlot() int64 {
	ic.freespaceMutex.Lock()
	defer ic.freespaceMutex.Unlock()
	if n := len(ic.freespace); n > 0 {
		pos := ic.freespace[n-1]
		ic.freespace = ic.freespace[:n-1]
		return pos
	}
	pos := ic.db_size
	ic.db_size += ENTRY_SIZE
	return pos
}

// arenaFreeSlot returns a memory-mode arena slot for reuse.
func (ic *Collection) arenaFreeSlot(pos int64) {
	if pos < 0 {
		return
	}
	ic.arenaMutex.Lock()
	defer ic.arenaMutex.Unlock()
	ic.arenaFree = append(ic.arenaFree, pos)
}
