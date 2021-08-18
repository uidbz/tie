// tie-common project tie-common.go
package request

import (
	"encoding/json"

	"git.sr.ht/~uid/tie/tiedb"
)

const (
	RequestTypeAdd = iota
	RequestTypeGet
	RequestTypeDelete
	RequestTypeUpdate
	RequestTypeBatch
	ReplyTypeEmpty
	ReplyTypeStatus
	ReplyTypeGet
	ReplyTypeBatch
)

type Reply struct {
	RequestString    string
	ReplyType        uint
	ReplyStruct      interface{}
	ReylpRawResponse json.RawMessage
}

type Request interface {
	Reply(*tiedb.Tree, tiedb.CollectionKey) (Reply, error)
}

func CreateReply(requestType uint) Reply {
	switch requestType {
	case RequestTypeAdd:
		return Reply{RequestString: "Add", ReplyType: ReplyTypeStatus, ReplyStruct: ReplyStatus{}}

	case RequestTypeGet:
		return Reply{RequestString: "Get", ReplyType: ReplyTypeGet, ReplyStruct: ReplyGet{}}

	case RequestTypeDelete:
		return Reply{RequestString: "Delete", ReplyType: ReplyTypeStatus, ReplyStruct: ReplyStatus{}}

	case RequestTypeUpdate:
		return Reply{RequestString: "Update", ReplyType: ReplyTypeStatus, ReplyStruct: ReplyStatus{}}

	case RequestTypeBatch:
		return Reply{RequestString: "Batch", ReplyType: ReplyTypeBatch, ReplyStruct: ReplyBatch{}}

	default:
		return Reply{RequestString: "Empty", ReplyType: ReplyTypeEmpty}
	}
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

type Batch struct {
	Add    []Add
	Get    []Get
	Delete []Delete
	Update []Update
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

type ReplyBatch struct {
	Add    []ReplyStatus
	Get    [][]*tiedb.StringSliceSet
	Delete []ReplyStatus
	Update []ReplyStatus
}
