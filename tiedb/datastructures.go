package tiedb

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"sync"
	"sync/atomic"
)

const (
	ENTRY_SIZE = SIZE_DATATYPE +
		SIZE_LEVEL +
		SIZE_ID +
		SIZE_PARENTID +
		SIZE_VALUE

	SIZE_DATATYPE = 2
	SIZE_LEVEL    = 8
	SIZE_ID       = 8
	SIZE_PARENTID = SIZE_ID
	SIZE_VALUE    = 24 // 6 * 8 - 3 * 8

	FILE_ADD    = 0
	FILE_DELETE = 1
	// FILE_BARRIER is not a record: it carries a Done channel that the writer
	// closes after fsyncing, once every write queued before it has been
	// applied. See Collection.Sync.
	FILE_BARRIER = 2

	// These values get written to the db files
	TYPE_DELETE      = 10
	TYPE_ENTRY       = 11
	TYPE_ASSOCIATION = 12
	TYPE_HASH        = 13

	// HASH_LEVEL is a sentinel level marking an entry ID that lives in the
	// whole-value hash store rather than in any trie level. A value routed
	// through BlobPolicy is stored as one 32-byte record and its Triple level
	// fields carry HASH_LEVEL, so getValue resolves it from hashEntries instead
	// of walking ParentId links.
	HASH_LEVEL = -1
)

type CollectionKey struct {
	Database   string
	Collection string
}

type RawDataEntry struct {
	Position int64
	Data     [ENTRY_SIZE]byte
}

type entryLevel struct {
	entries             *lockedTree[uint64, *UniqueValue]
	uniqueValues        *lockedTree[UniqueValue, uint64]
	associations        *lockedTree[uint64, *AssociationSet]
	reverseAssociations *lockedTree[uint64, *AssociationSet]
}

type Collection struct {
	dBPath            string
	dBName            string
	dBFullPath        string
	totalEntries      atomic.Uint64
	totalAssociations uint64

	secureLevel sync.RWMutex
	changeMutex sync.Mutex

	levels     []entryLevel
	levelCount int

	// blobPolicy, when non-nil, diverts matching values (e.g. content-address
	// hashes) out of the chunk trie and into the whole-value hash store below.
	// Copied from TieTree at initialize time; nil keeps the original trie-only
	// behavior. See [[BlobPolicy]].
	blobPolicy *BlobPolicy

	// hashEntries/hashValues back the whole-value hash store used when blobPolicy
	// matches. hashEntries maps an entry ID to its raw bytes; hashValues is the
	// reverse map used to dedup a value to its existing ID. Both are nil unless
	// blobPolicy is set. Entries here carry the HASH_LEVEL sentinel, not a real
	// trie level.
	hashEntries *lockedTree[uint64, [32]byte]
	hashValues  *lockedTree[[32]byte, uint64]

	// hashAssociations/hashReverseAssociations hold the association subtrees for
	// triples whose key (resp. value2) is a hash entry. Hash entries carry
	// HASH_LEVEL rather than a real trie level, so their associations cannot live
	// in the per-level entryLevel trees; these dedicated trees are their home.
	// Entry IDs are collection-global, so keying by ID here never collides with a
	// per-level tree. Both nil unless blobPolicy is set.
	hashAssociations        *lockedTree[uint64, *AssociationSet]
	hashReverseAssociations *lockedTree[uint64, *AssociationSet]

	// reverseRelations, when non-nil, restricts which relations (value1) get a
	// reverse-association index. A nil set means index every relation in reverse
	// (the original behavior). Limiting this near-halves association memory on
	// stores where most triples are never queried in reverse.
	reverseRelations map[string]bool

	// cache is non-nil only in disk-backed mode, where association trees keep
	// just an int64 position resident and the decoded Triple is read from disk.
	// It is a bounded LRU that spares that disk read for hot triples. In
	// memory-only mode Triples live in the arena and cache stays nil.
	cache *tripleCache

	// arena backs memory-only mode: association trees store an int64 position in
	// both modes, and in memory mode that position indexes into arena. This keeps
	// the association-tree value type uniform (int64) so nodes never box a Triple.
	// arenaFree holds slots freed by Delete for reuse, mirroring disk freespace.
	// arenaMutex guards both, since Add does not hold changeMutex for its whole
	// duration and may run concurrently with other Adds.
	arena      []Triple
	arenaFree  []int64
	arenaMutex sync.Mutex

	dBWriteQueue  chan FileMod
	dBReadQueue   chan ReadRequest
	dBCloseWriter chan bool

	// freespace holds disk slot offsets freed by Delete, for reuse by later Adds
	// (mirroring arenaFree in memory mode). Guarded by freespaceMutex: the load
	// pass pushes from concurrent workers, and the writer goroutine both pushes
	// (on delete) and pops (on add). There is no cap — every freed slot is
	// reclaimable, so the file does not grow while holes exist.
	freespace      []int64
	freespaceMutex sync.Mutex

	writeToDisk bool
	// readOnly opens the record file without a writer goroutine: reads are
	// served from a private read-only fd, writes return ErrReadOnly, and no
	// advisory lock is taken (so a reader can run beside a live writer). See
	// TieTree.SetReadOnly.
	readOnly bool
	// lock is the exclusive advisory lock on the record file, held for the
	// collection's lifetime when TieTree locking is enabled (see
	// TieTree.SetLocking). Release it through releaseLock, never by hand.
	lock *os.File
	// loadMemoryLimit is a soft heap ceiling (GOMEMLIMIT semantics) applied
	// while this collection loads from disk. 0 = runtime default.
	loadMemoryLimit int64
	finished        sync.WaitGroup
	// writerDone closes when the writer goroutine exits; Sync selects on it so
	// a barrier queued against an already-stopped writer cannot hang.
	writerDone chan struct{}
	// closed is set by closeDB; Sync returns immediately once set.
	closed atomic.Bool
	// file holds the open record file (a dbFile) owned by the writer
	// goroutine. It is an atomic.Value so tests can swap in a fault-injecting
	// handle without racing the writer.
	file atomic.Value
	// ioErr is the sticky write failure that put this collection read-only
	// (ENOSPC, a dying device). Reads keep working; writes report it. Guarded
	// by ioErrMutex; the first error is the one reported.
	ioErrMutex sync.Mutex
	ioErr      error
	db_size    int64

	// Free-text search: a transient, generation-invalidated corpus derived
	// from the in-memory index (see search.go). searchGen is bumped by the
	// user-write paths (addLocked/deleteLocked) and a repairing CheckIndex;
	// searchCache holds immutable corpora keyed by (relations, tie-types);
	// searchBytes is their approximate size, bounded by searchMaxBytes (0
	// disables caching); searchUse is the LRU clock.
	searchGen      atomic.Uint64
	searchMu       sync.Mutex
	searchCache    map[string]*searchCorpus
	searchBytes    int64
	searchMaxBytes int64
	searchUse      uint64
}

type UniqueValue struct {
	ParentId uint64
	Value    [SIZE_VALUE]byte
}

// BlobPolicy, when non-nil on a Collection, routes matching values to a
// whole-value hash entry instead of the 24-byte-chunk trie. Encode maps a value
// string to its raw bytes, returning ok=false for values that should use the
// trie; Decode is the inverse, reconstructing the string from stored bytes. Both
// must be deterministic on the value alone so a value's identity is stable across
// Adds (the same hash is the Key of many triples).
type BlobPolicy struct {
	Encode func(value string) (raw []byte, ok bool)
	Decode func(raw []byte) string
}

// HexHashBlobPolicy stores content-address hashes as raw 32-byte entries
// instead of chunking their 64-char hex form into the trie. A hash is the worst
// case for the chunk trie (no shared prefixes, 3 entries per hash); this
// collapses it to one. Encode matches only well-formed hex hashes; Decode
// re-hex-encodes the raw bytes back to the stored string.
//
// isContentAddress is supplied by the caller rather than imported: the shape
// rule belongs to the format package (metadata.IsHexHash), and the storage
// engine must not depend on it (§5.1 L21).
func HexHashBlobPolicy(isContentAddress func(string) bool) *BlobPolicy {
	return &BlobPolicy{
		Encode: func(value string) ([]byte, bool) {
			if !isContentAddress(value) {
				return nil, false
			}
			raw, err := hex.DecodeString(value)
			if err != nil {
				return nil, false
			}
			return raw, true
		},
		Decode: func(raw []byte) string {
			return hex.EncodeToString(raw)
		},
	}
}

type UniqueAssociation struct {
	AssociateTo uint64
	Relation    uint64
}

type Triple struct {
	Level       int
	Key         uint64
	Value1Level int
	Value1      uint64
	Value2Level int
	Value2      uint64
}

type StringTriple struct {
	Key    string
	Value1 string
	Value2 string
}

// Row is the flat, language-neutral result unit that crosses the wire. It groups
// one key's triples by relation: Attributes maps a relation (value1) to all its
// values (value2). A client reads row.Attributes["tag"] directly — no nested-map
// navigation or callbacks. TripleSet stays internal to the engine.
type Row struct {
	Key        string              `json:"key"`
	Attributes map[string][]string `json:"attributes"`
}

// RowsFromSorted folds an ordered []StringTriple into []Row, preserving the
// first-seen key order (the slice is already sorted/paginated by Sort). Triples
// for the same key coalesce into one Row; values under a relation keep their
// order of appearance.
func RowsFromSorted(sorted []StringTriple) []Row {
	rows := make([]Row, 0, len(sorted))
	index := make(map[string]int, len(sorted))
	for _, t := range sorted {
		i, ok := index[t.Key]
		if !ok {
			i = len(rows)
			index[t.Key] = i
			rows = append(rows, Row{Key: t.Key, Attributes: make(map[string][]string)})
		}
		rows[i].Attributes[t.Value1] = append(rows[i].Attributes[t.Value1], t.Value2)
	}
	return rows
}

type FileMod struct {
	Mode        int
	Level       int
	EntryType   int
	Position    int64
	Triple      *Triple
	EntryID     uint64
	UniqueValue *UniqueValue
	HashValue   [32]byte
	// Done closes once the writer has applied this mod. Only FILE_BARRIER sets
	// it.
	Done chan struct{}
}

type ReadRequest struct {
	Position  int64
	ReplyChan chan Triple
}

// EntryToBytes serializes a trie entry into the fixed ENTRY_SIZE frame:
// datatype(2) | level(8) | entryID(8) | parentId(8) | value(24). One buffer,
// written at the same fixed offsets the bufTo* decoders read — the old version
// allocated six tiny slices and appended them (and a discarded make) per record.
func EntryToBytes(level int, entryID uint64, uv *UniqueValue) []byte {
	var b [ENTRY_SIZE]byte
	binary.LittleEndian.PutUint16(b[0:SIZE_DATATYPE], TYPE_ENTRY)
	binary.LittleEndian.PutUint64(b[SIZE_DATATYPE:SIZE_DATATYPE+SIZE_LEVEL], uint64(level))
	next := SIZE_DATATYPE + SIZE_LEVEL + SIZE_ID
	binary.LittleEndian.PutUint64(b[next-SIZE_ID:next], entryID)
	binary.LittleEndian.PutUint64(b[next:next+SIZE_PARENTID], uv.ParentId)
	copy(b[next+SIZE_PARENTID:], uv.Value[:])
	return b[:]
}

// HashToBytes serializes a whole-value hash entry into the same 50-byte frame as
// the other record types: datatype(2) | level(8) | entryID(8) | raw hash(32).
// The 32-byte hash occupies the contiguous parentId+value region of the frame,
// so ENTRY_SIZE is unchanged. The level field is always HASH_LEVEL.
func HashToBytes(entryID uint64, raw [32]byte) []byte {
	var b [ENTRY_SIZE]byte
	hashLevel := int64(HASH_LEVEL) // -1 as the all-ones level the format stores
	binary.LittleEndian.PutUint16(b[0:SIZE_DATATYPE], TYPE_HASH)
	binary.LittleEndian.PutUint64(b[SIZE_DATATYPE:SIZE_DATATYPE+SIZE_LEVEL], uint64(hashLevel))
	next := SIZE_DATATYPE + SIZE_LEVEL + SIZE_ID
	binary.LittleEndian.PutUint64(b[next-SIZE_ID:next], entryID)
	copy(b[next:], raw[:])
	return b[:]
}

// toBytes serializes an association record: datatype(2) | entry level(8) |
// entry id(8) | value2 level(8) | value2(8) | value1 level(8) | value1(8).
func (a *Triple) toBytes() []byte {
	var b [ENTRY_SIZE]byte
	binary.LittleEndian.PutUint16(b[0:SIZE_DATATYPE], TYPE_ASSOCIATION)
	off := SIZE_DATATYPE
	put := func(v uint64) {
		binary.LittleEndian.PutUint64(b[off:off+8], v)
		off += 8
	}
	put(uint64(a.Level))
	put(a.Key)
	put(uint64(a.Value2Level))
	put(a.Value2)
	put(uint64(a.Value1Level))
	put(a.Value1)
	return b[:]
}
