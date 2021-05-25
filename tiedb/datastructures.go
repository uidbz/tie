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

type Collection interface {
	Collection(name string) Collection
	Add(key, value1, value2 string) *Association
	GetAssociations(value string) (bool, *Tree)
	GetAssociationsExt(value string, entryCollection string) (bool, *Tree)
	SetToString(value string, relationFilter string, s *Tree) (*StringSliceSet, []*Tree)
	Update(key string, value1 string, value2 string, newValue2 string) (bool, string)
	Delete(key string, value1 string, value2 string) (bool, string)
	CloseDB()
}

type InternalCollection struct {
	TotalEntries             uint64
	TotalAsses               uint64
	mu                       sync.Mutex // TODO: Better names
	mu2                      sync.Mutex // TODO: Better names
	mutexAssociation         sync.Mutex // TODO: Better names
	mutexInsert              sync.Mutex // TODO: Better names
	InserterWG               sync.WaitGroup
	InserterWGAssociation    sync.WaitGroup
	InserterWGAssociationExt sync.WaitGroup
	InserterWGDynTrie        sync.WaitGroup
	data                     chan []byte    // TODO: Better names
	wg                       sync.WaitGroup // TODO: Better names

	Root                Entry
	RootAss             Association
	Entries             []*Tree
	EntryAdder          []chan *Entry
	AssociationAdder    []chan *Association
	AssociationExtAdder []chan *AssociationExt
	UniqueValues        []*Tree
	Values              *Tree
	Associations        []*Tree
	AssociationsExt     []*Tree
	SubCollections      []*Tree
	Freespace           chan FileEntry
	DBPath              string
	DBName              string
	DBFullPath          string
	DBWriteQueue        chan FileMod
	DBCloseWriter       chan bool
	WriteToDisk         bool
	// Finished            chan bool
	Finished sync.WaitGroup
	// DBUpdateQueue    chan FileEntry
	// DBDeleteQueue    chan FileEntry
	db_size int64
	Level   int
	Id      uint64
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

type StringSliceSet struct {
	Item         string
	Keys         []string
	Associations []string
	Relations    []string
	// Good idea?
	// CustomColumn []CustomColumn //ArbitraryColumns
}

// type CustomColumn struct {
// 	Name string
// 	Values []string
// }

type FileEntry interface {
	ToBytes() []byte
	SetPosition(int64)
	GetPosition() int64
}

type FileMod struct {
	Mode  int
	Entry FileEntry
}

func (e *Entry) ToBytes() []byte {
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

func (e *Entry) SetPosition(pos int64) {
	e.Position = pos
}

func (e *Entry) GetPosition() int64 {
	return e.Position
}

func (a *Association) ToBytes() []byte {
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

func (a *Association) SetPosition(pos int64) {
	a.Position = pos
}

func (a *Association) GetPosition() int64 {
	return a.Position
}

func (a *AssociationExt) ToBytes() []byte {
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

func (a *AssociationExt) SetPosition(pos int64) {
	a.Position = pos
}

func (a *AssociationExt) GetPosition() int64 {
	return a.Position
}
