package tiedb

import (
	"encoding/binary"
	"strings"
	"sync"
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

	// ASSOCIATED = "associated"
)

type FileIndexer interface {
	defaultRelations() []string
	defaultHandlers() []string
	defaultTypes() []string
}

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
	totalEntries      uint64
	totalAssociations uint64

	secureLevel       sync.RWMutex
	totalEntriesMutex sync.Mutex
	changeMutex       sync.Mutex

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

	writeToDisk    bool
	finished       sync.WaitGroup
	finishedAdding sync.WaitGroup
	db_size        int64
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

type Unit struct{}
type Value2 map[string]Unit
type Value1 map[string]Value2
type TripleSet map[string]Value1

func (s TripleSet) Has(key string) bool {
	_, ok := s[key]
	return ok
}

func (s TripleSet) ForEachKey(do func(key string)) {
	for key, _ := range s {
		do(key)
	}
}

func (s TripleSet) ForEachValue1(do func(key, value1 string)) {
	for key, keys := range s {
		for value1, _ := range keys {
			do(key, value1)
		}
	}
}

func (s TripleSet) ForEachValue2(do func(key, value1, value2 string)) {
	for key, keys := range s {
		for value1, value1s := range keys {
			for value2, _ := range value1s {
				do(key, value1, value2)
			}
		}
	}
}

func (s Value1) Has(value1 string) bool {
	_, ok := s[value1]
	return ok
}

func (s Value1) ForEach(do func(value1 string)) {
	for value1, _ := range s {
		do(value1)
	}
}

func (s Value1) ForEachValue2(do func(value1, value2 string)) {
	for value1, value1s := range s {
		for value2, _ := range value1s {
			do(value1, value2)
		}
	}
}

// There must exist exactly 1 value2 for the provided value1! Otherwise it will return (empty string, false)")
func (s Value2) One() (string, bool) {
	if len(s) != 1 {
		return "", false
	}
	for value2, _ := range s {
		return value2, true
	}
	return "", false
}

func (s Value2) Has(value2 string) bool {
	_, ok := s[value2]
	return ok
}

func (s Value2) ToString() string {
	return strings.Join(s.ToSlice(), ", ")
}

func (s Value2) ToSlice() []string {
	tmp := make([]string, 0, len(s))
	for value2 := range s {
		tmp = append(tmp, value2)
	}
	return tmp
}

func (s Value2) ForEach(do func(value2 string)) {
	for value2, _ := range s {
		do(value2)
	}
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
}

type ReadRequest struct {
	Position  int64
	ReplyChan chan Triple
}

func EntryToBytes(level int, entryID uint64, uv *UniqueValue) []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	levelBytes := make([]byte, SIZE_LEVEL)
	id := make([]byte, SIZE_ID)
	parentId := make([]byte, SIZE_PARENTID)

	binary.LittleEndian.PutUint16(datatype, TYPE_ENTRY)
	binary.LittleEndian.PutUint64(levelBytes, uint64(level))
	binary.LittleEndian.PutUint64(id, entryID)
	binary.LittleEndian.PutUint64(parentId, uv.ParentId)

	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], levelBytes[:]...)
	out = append(out, id[:]...)
	out = append(out, parentId[:]...)
	out = append(out, uv.Value[:]...)

	return out
}

// HashToBytes serializes a whole-value hash entry into the same 50-byte frame as
// the other record types: datatype(2) | level(8) | entryID(8) | raw hash(32).
// The 32-byte hash occupies the contiguous parentId+value region of the frame,
// so ENTRY_SIZE is unchanged. The level field is always HASH_LEVEL.
func HashToBytes(entryID uint64, raw [32]byte) []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	levelBytes := make([]byte, SIZE_LEVEL)
	id := make([]byte, SIZE_ID)

	level := int64(HASH_LEVEL)
	binary.LittleEndian.PutUint16(datatype, TYPE_HASH)
	binary.LittleEndian.PutUint64(levelBytes, uint64(level))
	binary.LittleEndian.PutUint64(id, entryID)

	out := append(datatype[:], levelBytes[:]...)
	out = append(out, id[:]...)
	out = append(out, raw[:]...)

	return out
}

func (a *Triple) toBytes() []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	entry_level := make([]byte, SIZE_LEVEL)
	entry_id := make([]byte, SIZE_ID)
	association_level := make([]byte, SIZE_ID)
	association := make([]byte, SIZE_ID)
	relation_level := make([]byte, SIZE_LEVEL)
	relation := make([]byte, SIZE_ID)

	binary.LittleEndian.PutUint16(datatype, TYPE_ASSOCIATION)
	binary.LittleEndian.PutUint64(entry_level, uint64(a.Level))
	binary.LittleEndian.PutUint64(entry_id, a.Key)
	binary.LittleEndian.PutUint64(association_level, uint64(a.Value2Level))
	binary.LittleEndian.PutUint64(association, a.Value2)
	binary.LittleEndian.PutUint64(relation_level, uint64(a.Value1Level))
	binary.LittleEndian.PutUint64(relation, a.Value1)

	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], entry_level[:]...)
	out = append(out, entry_id[:]...)
	out = append(out, association_level[:]...)
	out = append(out, association[:]...)
	out = append(out, relation_level[:]...)
	out = append(out, relation[:]...)

	return out
}
