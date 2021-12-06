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
	ReplyRawResponse json.RawMessage
}

type Request interface {
	Reply(*tiedb.Tree, tiedb.CollectionKey) (Reply, error)
	RequestType() uint
}

func CreateReply(requestType uint) Reply {
	switch requestType {
	case RequestTypeAdd:
		return Reply{RequestString: "Add", ReplyType: ReplyTypeStatus, ReplyStruct: ReplyStatus{}}

	case RequestTypeGet:
		return Reply{RequestString: "Get", ReplyType: ReplyTypeGet, ReplyStruct: []ReplyGet{}}

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

func (r *Reply) DataGet() *[]ReplyGet {
	return r.ReplyStruct.(*[]ReplyGet)
}

func (r *Reply) DataStatus() *ReplyStatus {
	return r.ReplyStruct.(*ReplyStatus)
}

func (r *Reply) DataBatch() *ReplyBatch {
	return r.ReplyStruct.(*ReplyBatch)
}

func NewAddRequest(key, value1, value2 string) *Add {
	return &Add{
		Key:         key,
		Value1:      value1,
		Value2:      value2,
		requestType: RequestTypeAdd,
	}
}

func NewGetRequest(keys []string) *Get {
	return &Get{
		Values:      keys,
		requestType: RequestTypeGet,
	}
}

func NewDeleteRequest(key, value1, value2 string) *Delete {
	return &Delete{
		Key:         key,
		Value1:      value1,
		Value2:      value2,
		requestType: RequestTypeDelete,
	}
}

func NewUpdateRequest() *Update {
	return &Update{
		requestType: RequestTypeUpdate,
	}
}

func NewBatchRequest() *Batch {
	return &Batch{
		requestType: RequestTypeBatch,
	}
}

type Add struct {
	Key         string
	Value1      string
	Value2      string
	requestType uint
}

func (a *Add) RequestType() uint {
	return a.requestType
}

type Get struct {
	Values          []string
	NextLevelValues []string
	Filter          string
	MaxAssociations int
	requestType     uint
}

func (g *Get) RequestType() uint {
	return g.requestType
}

type Delete struct {
	Key         string
	Value1      string
	Value2      string
	requestType uint
}

func (d *Delete) RequestType() uint {
	return d.requestType
}

type Update struct {
	Key          string
	Value1       string
	Value2       string
	NewValue2    string
	AddOnFailure bool
	requestType  uint
}

func (u *Update) RequestType() uint {
	return u.requestType
}

type Batch struct {
	Add         []*Add
	Get         []*Get
	Delete      []*Delete
	Update      []*Update
	requestType uint
}

func (b *Batch) RequestType() uint {
	return b.requestType
}

type ReplyGetSlice []struct {
	Result []ReplyGet
}

type ReplyGet struct {
	Item   string
	Key    []string
	Value1 []string
	Value2 []string
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
