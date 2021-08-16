// tie-common project tie-common.go
package request

import (
	"git.sr.ht/~uid/tie/tiedb"
)

const (
	ReplyTypeEmpty = iota
	ReplyTypeStatus
	ReplyTypeGet
	ReplyTypeBatch
)

type Reply struct {
	ReplyType   uint
	ReplyStruct interface{}
}

type Request interface {
	Reply(*tiedb.Tree, tiedb.CollectionKey) (Reply, error)
}

type Batch struct {
	Add    []Add
	Get    []Get
	Delete []Delete
	Update []Update
}

type ReplyBatch struct {
	Add    []ReplyStatus
	Get    [][]*tiedb.StringSliceSet
	Delete []ReplyStatus
	Update []ReplyStatus
}

type Add struct {
	Key    string
	Value1 string
	Value2 string
}

type Get struct {
	Values          []string
	NextLevelValues []string
	Filter          string
	MaxAssociations int
}

type Delete struct {
	Key    string
	Value1 string
	Value2 string
}

type Update struct {
	Key          string
	Value1       string
	Value2       string
	NewValue2    string
	AddOnFailure bool
}

type ReplyGet struct {
	Item         string
	Associations []string
	Relations    []string
}

type ReplyStatus struct {
	Success    bool
	Message    string
	OrigKey    string
	OrigValue1 string
	OrigValue2 string
}
