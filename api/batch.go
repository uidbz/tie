package api

import (
	"errors"
	"strconv"

	ws "github.com/uidbz/tie/webservice"
)

const (
	IdBatch = "Batch"

	BatchAdd    = "add"
	BatchDelete = "delete"
	BatchSet    = "set"
	BatchUpdate = "update"
)

// BatchOp is one write in a batch. Op selects the kind; the remaining fields are
// interpreted per Op:
//   - add:    add triple (Key, Relation, Values[0])
//   - delete: delete triple (Key, Relation, Values[0])
//   - set:    replace (Key, Relation) with all of Values
//   - update: change (Key, Relation, Values[0]) to NewValue; AddOnFailure adds
//     NewValue when the original triple is absent
type BatchOp struct {
	Op           string   `json:"op"`
	Key          string   `json:"key"`
	Relation     string   `json:"relation"`
	Values       []string `json:"values"`
	NewValue     string   `json:"newValue"`
	AddOnFailure bool     `json:"addOnFailure"`
}

// Batch is an ordered list of write ops against one collection. Ops execute in
// slice order (the caller controls sequencing — e.g. a delete before an add to
// replace a value); every op is applied and checked, and the whole batch shares
// one durability Sync at the end.
type Batch struct {
	Collection CollectionInfo `json:"collection"`
	Ops        []BatchOp      `json:"ops"`
}

type BatchRequest struct {
	ws.Request
	Batch *Batch `json:"batch"`
}

type BatchReply struct {
	ws.ReplyStatus
}

func (batch *Batch) Add(key, relation, value string) {
	batch.Ops = append(batch.Ops, BatchOp{Op: BatchAdd, Key: key, Relation: relation, Values: []string{value}})
}

func (batch *Batch) Delete(key, relation, value string) {
	batch.Ops = append(batch.Ops, BatchOp{Op: BatchDelete, Key: key, Relation: relation, Values: []string{value}})
}

func (batch *Batch) Set(key, relation string, values []string) {
	batch.Ops = append(batch.Ops, BatchOp{Op: BatchSet, Key: key, Relation: relation, Values: values})
}

func (batch *Batch) Update(update Update) {
	batch.Ops = append(batch.Ops, BatchOp{
		Op:           BatchUpdate,
		Key:          update.Key,
		Relation:     update.Value1,
		Values:       []string{update.Value2},
		NewValue:     update.NewValue2,
		AddOnFailure: update.AddOnFailure,
	})
}

func (request *BatchRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := BatchReply{}
	col := env.Collection(request.Batch.Collection.Namespace, request.Batch.Collection.CollectionId)

	for i, op := range request.Batch.Ops {
		var msg string
		ok := true
		switch op.Op {
		case BatchAdd:
			col.Add(op.Key, op.Relation, op.value())
		case BatchDelete:
			// Delete is idempotent: a missing triple is a no-op, not a batch
			// failure. Callers delete prior values (tag-date, tags) that may
			// not exist yet on first write.
			col.Delete(op.Key, op.Relation, op.value())
		case BatchSet:
			col.SetValues(op.Key, op.Relation, op.Values)
		case BatchUpdate:
			if op.AddOnFailure {
				msg, ok = col.UpdateAdd(op.Key, op.Relation, op.value(), op.NewValue)
			} else {
				msg, ok = col.Update(op.Key, op.Relation, op.value(), op.NewValue)
			}
		default:
			msg, ok = "unknown batch op '"+op.Op+"'", false
		}
		if !ok {
			reply.Success = false
			reply.Message = "batch op " + strconv.Itoa(i) + " (" + op.Op + ") failed: " + msg
			col.Sync()
			return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, errors.New(reply.Message)
		}
	}

	col.Sync()
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (op BatchOp) value() string {
	if len(op.Values) == 0 {
		return ""
	}
	return op.Values[0]
}

func (request *BatchRequest) New() ws.RequestInterface {
	return NewBatchRequest(&Batch{})
}

func NewBatchRequest(batch *Batch) *BatchRequest {
	if batch == nil {
		return nil
	}
	request := &BatchRequest{}
	request.Id = IdBatch
	request.ReplyStructPtr = &BatchReply{}
	request.Batch = batch

	return request
}
