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

	FILE_APPEND = iota
	FILE_UPDATE
	FILE_DELETE

	// Do not change order; these values get written to the db files
	TYPE_DELETE = iota
	TYPE_ENTRY
	TYPE_ASSOCIATION
	TYPE_ASSOCIATIONEXT

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

// type Collection interface {
// 	Collection(name string) Collection
// 	Add(key, value1, value2 string) *Association
// 	Get(key string, value1 string) (bool, *StringSliceSet)
// 	GetAssociations(value string) (bool, *Tree)
// 	GetAssociationsExt(value string, entryCollection string) (bool, *Tree)
// 	SetToString(value string, relationFilter string, s *Tree) (*StringSliceSet, []*Tree)
// 	SetToString2(value string, relationFilter string, s *Tree) (*TrippleSet, []*Tree)
// 	Update(key string, value1 string, value2 string, newValue2 string) (bool, string)
// 	UpdateAdd(key string, value1 string, value2 string, newValue2 string) (bool, string)
// 	SimpleUpdate(key string, value1 string, newValue2 string, addOnFail bool) (bool, string)
// 	SimpleUpdateUsingSet(key string, value1 string, newValue2 string, addOnFail bool, set *StringSliceSet) (bool, string)
// 	Delete(key string, value1 string, value2 string) (bool, string)
// 	CloseDB()
// }

type Collection struct {
	dBPath            string
	dBName            string
	dBFullPath        string
	totalEntries      uint64
	totalAssociations uint64

	changeMutex sync.Mutex

	rawDataToLoad chan []byte
	allDataLoaded sync.WaitGroup // TODO: Better names

	root    Entry
	rootAss Association

	entryAdder          []chan *Entry
	associationAdder    []chan *Association
	associationExtAdder []chan *AssociationExt

	inserterWG               sync.WaitGroup
	inserterWGAssociation    sync.WaitGroup
	inserterWGAssociationExt sync.WaitGroup
	inserterWGDynTrie        sync.WaitGroup

	entries         []*TieTree
	uniqueValues    []*TieTree
	values          *TieTree
	associations    []*TieTree
	associationsExt []*TieTree
	subCollections  []*TieTree

	dBWriteQueue  chan FileMod
	dBCloseWriter chan bool
	freespace     chan FileEntry
	writeToDisk   bool
	finished      sync.WaitGroup
	db_size       int64
	level         int
	id            uint64
}

type UniqueValue struct {
	ParentId uint64
	Value    *[]byte
}

type UniqueAssociation struct {
	// AssociateToCollectionLevel int
	// AssociateToCollection uint64
	AssociateTo uint64
	// RelationCollectionLevel    int
	// RelationCollection uint64
	Relation uint64
}

type UniqueAssociationExt struct {
	// AssociateToCollectionLevel int
	AssociateToCollection uint64
	AssociateTo           uint64
	// RelationCollectionLevel int
	RelationCollection uint64
	Relation           uint64
}

type Entry struct {
	Id          uint64
	Level       int
	UniqueValue UniqueValue
	Position    int64
}

type Association struct {
	// CollectionLevel            int
	// Collection uint64
	EntryId uint64
	Level   int
	// AssociationCollectionLevel int
	// AssociationCollection      uint64
	AssociationLevel int
	AssociateTo      uint64
	// RelationCollectionLevel    int
	// RelationCollection         uint64
	RelationLevel int
	Relation      uint64
	Position      int64
}

type AssociationExt struct {
	AssociateTo                uint64
	Relation                   uint64
	AssociationCollectionLevel int
	AssociationCollection      uint64
	RelationCollectionLevel    int
	RelationCollection         uint64
	Position                   int64
}

type Set map[string]map[string]map[string]bool

type StringSliceSet struct {
	Item   string
	Key    []string
	Value1 []string
	Value2 []string
	// Good idea?
	// CustomColumn []CustomColumn //ArbitraryColumns
}

// type CustomColumn struct {
// 	Name string
// 	Values []string
// }

// Returns first Value1 or empty string if non-existant
func (set *StringSliceSet) FirstValue1() string {
	if set != nil && len(set.Value1) > 0 {
		return set.Value1[0]
	}
	return ""
}

// Returns first Value2 or empty string if non-existant
func (set *StringSliceSet) FirstValue2() string {
	if set != nil && len(set.Value2) > 0 {
		return set.Value2[0]
	}
	return ""
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

func (s Value2) ForEach(do func(value2 string)) {
	for value2, _ := range s {
		do(value2)
	}
}

type FileEntry interface {
	toBytes() []byte
	setPosition(int64)
	getPosition() int64
}

type FileMod struct {
	Mode  int
	Entry FileEntry
}

func (e *Entry) toBytes() []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	level := make([]byte, SIZE_LEVEL)
	id := make([]byte, SIZE_ID)
	parentId := make([]byte, SIZE_PARENTID)
	value := make([]byte, SIZE_VALUE)

	binary.LittleEndian.PutUint16(datatype, TYPE_ENTRY)
	binary.LittleEndian.PutUint64(level, uint64(e.Level))
	binary.LittleEndian.PutUint64(id, e.Id)
	binary.LittleEndian.PutUint64(parentId, e.UniqueValue.ParentId)
	copy(value, *e.UniqueValue.Value)
	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], level[:]...)
	out = append(out, id[:]...)
	out = append(out, parentId[:]...)
	out = append(out, value[:]...)

	return out
}

func (e *Entry) setPosition(pos int64) {
	e.Position = pos
}

func (e *Entry) getPosition() int64 {
	return e.Position
}

func (a *Association) toBytes() []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	entry_level := make([]byte, SIZE_LEVEL)
	entry_id := make([]byte, SIZE_ID)
	association_level := make([]byte, SIZE_ID)
	association := make([]byte, SIZE_ID)
	relation_level := make([]byte, SIZE_LEVEL)
	relation := make([]byte, SIZE_ID)

	binary.LittleEndian.PutUint16(datatype, TYPE_ASSOCIATION)
	binary.LittleEndian.PutUint64(entry_level, uint64(a.Level))
	binary.LittleEndian.PutUint64(entry_id, a.EntryId)
	binary.LittleEndian.PutUint64(association_level, uint64(a.AssociationLevel))
	binary.LittleEndian.PutUint64(association, a.AssociateTo)
	binary.LittleEndian.PutUint64(relation_level, uint64(a.RelationLevel))
	binary.LittleEndian.PutUint64(relation, a.Relation)

	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], entry_level[:]...)
	out = append(out, entry_id[:]...)
	out = append(out, association_level[:]...)
	out = append(out, association[:]...)
	out = append(out, relation_level[:]...)
	out = append(out, relation[:]...)

	return out
}

func (a *Association) setPosition(pos int64) {
	a.Position = pos
}

func (a *Association) getPosition() int64 {
	return a.Position
}

func (a *AssociationExt) toBytes() []byte {
	datatype := make([]byte, SIZE_DATATYPE)
	association := make([]byte, SIZE_ID)
	relation := make([]byte, SIZE_ID)
	association_collection_level := make([]byte, SIZE_LEVEL)
	association_collection := make([]byte, SIZE_ID)
	relation_collection_level := make([]byte, SIZE_LEVEL)
	relation_collection := make([]byte, SIZE_ID)

	binary.LittleEndian.PutUint16(datatype, TYPE_ASSOCIATIONEXT)
	binary.LittleEndian.PutUint64(association, a.AssociateTo)
	binary.LittleEndian.PutUint64(relation, a.Relation)
	binary.LittleEndian.PutUint64(association_collection_level, uint64(a.AssociationCollectionLevel))
	binary.LittleEndian.PutUint64(association_collection, a.AssociationCollection)
	binary.LittleEndian.PutUint64(relation_collection_level, uint64(a.RelationCollectionLevel))
	binary.LittleEndian.PutUint64(relation_collection, a.RelationCollection)

	out := make([]byte, ENTRY_SIZE)

	out = append(datatype[:], association[:]...)
	out = append(out, relation[:]...)
	out = append(out, association_collection_level[:]...)
	out = append(out, association_collection[:]...)
	out = append(out, relation_collection_level[:]...)
	out = append(out, relation_collection[:]...)

	return out
}

func (a *AssociationExt) setPosition(pos int64) {
	a.Position = pos
}

func (a *AssociationExt) getPosition() int64 {
	return a.Position
}
