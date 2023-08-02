package tiedb

import (
	"encoding/binary"
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
	// SIZE_VALUE    = 72 // 12 * 8 - 3 * 8
	SIZE_VALUE = 24 // 6 * 8 - 3 * 8

	MaxFreespace = 1000000

	FILE_ADD    = 0
	FILE_DELETE = 1

	// These values get written to the db files
	TYPE_DELETE      = 10
	TYPE_ENTRY       = 11
	TYPE_ASSOCIATION = 12

	ASSOCIATED = "associated"
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
	Data     []byte
}

type Collection struct {
	dBPath            string
	dBName            string
	dBFullPath        string
	totalEntries      uint64
	totalAssociations uint64

	rawDataToLoad chan RawDataEntry
	allDataLoaded sync.WaitGroup

	secureLevel       sync.Mutex
	totalEntriesMutex sync.Mutex
	changeMutex       sync.Mutex

	entries      []*TieTree
	uniqueValues []*TieTree
	associations []*TieTree

	dbReadWg       sync.WaitGroup
	dBWriteQueue   chan FileMod
	dBReadQueue    chan ReadRequest
	dBCloseWriter  chan bool
	freespace      chan int64
	writeToDisk    bool
	finished       sync.WaitGroup
	finishedAdding sync.WaitGroup
	db_size        int64
	level          int
	id             uint64
}

type UniqueValue struct {
	ParentId uint64
	Value    [SIZE_VALUE]byte
}

type UniqueAssociation struct {
	AssociateTo uint64
	Relation    uint64
}

type Entry struct {
	Id          uint64
	UniqueValue UniqueValue
}

type Triple struct {
	Level       int
	Key         uint64
	Value1Level int
	Value1      uint64
	Value2Level int
	Value2      uint64
}

type Set map[string]map[string]map[string]bool

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
	Association *Triple
	Entry       *Entry
}

type ReadRequest struct {
	Position  int64
	ReplyChan chan Triple
}

func (e *Entry) toBytes(level int) []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	levelBytes := make([]byte, SIZE_LEVEL)
	id := make([]byte, SIZE_ID)
	parentId := make([]byte, SIZE_PARENTID)

	binary.LittleEndian.PutUint16(datatype, TYPE_ENTRY)
	binary.LittleEndian.PutUint64(levelBytes, uint64(level))
	binary.LittleEndian.PutUint64(id, e.Id)
	binary.LittleEndian.PutUint64(parentId, e.UniqueValue.ParentId)

	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], levelBytes[:]...)
	out = append(out, id[:]...)
	out = append(out, parentId[:]...)
	out = append(out, e.UniqueValue.Value[:]...)

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
