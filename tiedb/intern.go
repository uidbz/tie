package tiedb

// Value interning: the trie of unique values (values are stored once,
// as a chain of per-level entries), the blob/hash level, and value lookup.

import (
	"bytes"
	"sync/atomic"
)

// insertHash stores raw under a fresh ID (or returns the existing ID when the
// value is already interned) and queues its TYPE_HASH record in disk mode. The
// caller holds changeMutex.
func (ic *Collection) insertHash(raw [32]byte) uint64 {
	if id, found := ic.hashValues.Get(raw); found {
		return id
	}
	id := ic.nextID()
	ic.hashEntries.Put(id, raw)
	ic.hashValues.Put(raw, id)
	if ic.writeToDisk {
		ic.queueWrite(FileMod{
			EntryType: TYPE_HASH,
			Mode:      FILE_ADD,
			EntryID:   id,
			HashValue: raw,
			Position:  ic.allocSlot(),
		})
	}
	return id
}

// loadHash reinserts a hash entry read from disk, keeping totalEntries in step
// with the highest seen ID (mirrors the TYPE_ENTRY load path).
func (ic *Collection) loadHash(entryID uint64, raw [32]byte) {
	ic.totalEntriesMutex.Lock()
	if entryID > ic.totalEntries {
		ic.totalEntries = entryID
	}
	ic.totalEntriesMutex.Unlock()
	ic.hashEntries.Put(entryID, raw)
	ic.hashValues.Put(raw, entryID)
}

// asBlob returns the raw bytes for value when the blob policy matches it and the
// bytes fit the fixed 32-byte hash slot. A policy match whose encoding is not 32
// bytes falls through to the trie rather than corrupting the fixed frame.
func (ic *Collection) asBlob(value string) ([32]byte, bool) {
	if ic.blobPolicy == nil {
		return [32]byte{}, false
	}
	raw, ok := ic.blobPolicy.Encode(value)
	if !ok || len(raw) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(raw), true
}

func (ic *Collection) secureLevelInIndex(level int) {
	ic.secureLevel.RLock()
	enough := level < ic.levelCount
	ic.secureLevel.RUnlock()
	if enough {
		return
	}

	ic.secureLevel.Lock()
	defer ic.secureLevel.Unlock()

	for i := ic.levelCount; i <= level; i++ {
		ic.levels = append(ic.levels, entryLevel{
			entries:             newLockedTree[uint64, *UniqueValue](uint64Hash),
			uniqueValues:        newLockedTree[UniqueValue, uint64](uniqueValueHash),
			associations:        newLockedTree[uint64, *AssociationSet](uint64Hash),
			reverseAssociations: newLockedTree[uint64, *AssociationSet](uint64Hash),
		})
		ic.levelCount++
	}
}

// levelAt returns the entryLevel at level under a read lock, guarding against a
// concurrent secureLevelInIndex append reallocating the levels slice. The
// second return is false when level is out of range.
func (ic *Collection) levelAt(level int) (entryLevel, bool) {
	ic.secureLevel.RLock()
	defer ic.secureLevel.RUnlock()
	if level < 0 || level >= ic.levelCount {
		return entryLevel{}, false
	}
	return ic.levels[level], true
}

func (ic *Collection) valueExists(level int, parentId uint64, value [SIZE_VALUE]byte) (uint64, bool) {
	ic.secureLevelInIndex(level)

	lvl, ok := ic.levelAt(level)
	if !ok {
		return 0, false
	}
	if entryID, found := lvl.uniqueValues.Get(UniqueValue{parentId, value}); found {
		return entryID, true
	} else {
		return 0, false
	}
}

func (ic *Collection) insert(value string) (entryID uint64, level int) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()
	return ic.insertLocked(value)
}

// insertLocked interns value into the trie; the caller must hold changeMutex.
func (ic *Collection) insertLocked(value string) (entryID uint64, level int) {

	if raw, ok := ic.asBlob(value); ok {
		return ic.insertHash(raw), HASH_LEVEL
	}

	var lastParentID uint64
	bytes := []byte(value)
	checkExistance := true
	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var tmp uint64

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level = i / SIZE_VALUE

		value := ([SIZE_VALUE]byte)(bytes[i:end])

		if checkExistance {
			tmp, checkExistance = ic.valueExists(level, lastParentID, value)
		}
		if checkExistance {
			lastParentID = tmp
		} else {
			checkExistance = false
			lastParentID = ic.insertValue(level, lastParentID, value)
		}
	}

	return lastParentID, level
}

func (ic *Collection) getEntryFromString(value string) (entryID uint64, level int, found bool) {
	if raw, ok := ic.asBlob(value); ok {
		if id, exists := ic.hashValues.Get(raw); exists {
			return id, HASH_LEVEL, true
		}
		return 0, 0, false
	}

	bytes := []byte(value)

	align := make([]byte, SIZE_VALUE-len(bytes)%SIZE_VALUE)
	bytes = append(bytes, align...)

	var lastParentId uint64
	var lastLevel int

	for i := 0; i < len(bytes); i = i + SIZE_VALUE {
		end := i + SIZE_VALUE
		level := i / SIZE_VALUE

		value := ([SIZE_VALUE]byte)(bytes[i:end])

		if tmp, exists := ic.valueExists(level, lastParentId, value); exists {
			lastParentId = tmp
			lastLevel = level
		} else {
			return 0, 0, false
		}
	}

	return lastParentId, lastLevel, true
}

func (ic *Collection) GetTotalEntries() uint64 {
	return ic.totalEntries
}

func (ic *Collection) nextID() uint64 {
	return atomic.AddUint64(&ic.totalEntries, 1)
}

func (ic *Collection) insertValue(level int, parentId uint64, value [SIZE_VALUE]byte) (entryID uint64) {
	entryID = ic.nextID()
	uv := &UniqueValue{parentId, value}

	if ic.writeToDisk {
		m := FileMod{
			EntryType:   TYPE_ENTRY,
			Mode:        FILE_ADD,
			Level:       level,
			EntryID:     entryID,
			UniqueValue: uv,
			Position:    ic.allocSlot(),
		}
		ic.queueWrite(m)
	}
	ic.insertEntry(level, entryID, uv)

	return entryID
}

func (ic *Collection) insertEntry(level int, id uint64, uv *UniqueValue) {
	ic.secureLevelInIndex(level)
	lvl, _ := ic.levelAt(level) // level exists after secureLevelInIndex
	lvl.entries.Put(id, uv)
	lvl.uniqueValues.Put(*uv, id)
}

func (ic *Collection) getUniqueValue(level int, id uint64) *UniqueValue {
	if lvl, ok := ic.levelAt(level); ok {
		if entry, found := lvl.entries.Get(id); found {
			return entry
		}
	}
	return nil
}

// Returns a copy of a full value. A missing entry at any level yields nil:
// CheckIndex deliberately samples dangling ids, and a nil-deref here would
// crash the very report that exists to surface them.
func (ic *Collection) getValue(level int, id uint64) []byte {
	if level < 0 {
		return []byte{}
	}
	if level == 0 {
		if uv := ic.getUniqueValue(0, id); uv != nil {
			return uv.Value[:]
		}
		return nil
	}
	uv := ic.getUniqueValue(level, id)
	if uv != nil {
		return append(ic.getValue(level-1, uv.ParentId), uv.Value[:]...)
	}
	return nil
}

func (ic *Collection) getValueString(level int, entryID uint64) string {
	if level == HASH_LEVEL {
		if raw, found := ic.hashEntries.Get(entryID); found {
			return ic.blobPolicy.Decode(raw[:])
		}
		return ""
	}

	value := ic.getValue(level, entryID)
	value = bytes.Trim(value, "\x00")

	return string(value)
}
