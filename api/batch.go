package api

import (
	"errors"

	tiedb "github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdBatch = "Batch"

	BatchAdd         = "add"
	BatchDelete      = "delete"
	BatchSet         = "set"
	BatchUpdate      = "update"
	BatchUpdateAdd   = "update-add"
	BatchAddIfAbsent = "add-if-absent"
)

// BatchOp is one write in a batch. Op selects the kind; the remaining fields are
// interpreted per Op:
//   - add:          add triple (Key, Relation, Values[0])
//   - delete:       delete triple (Key, Relation, Values[0]); absent is a no-op
//   - set:          replace (Key, Relation) with all of Values
//   - update:       change (Key, Relation, Values[0]) to NewValue; fails the
//     batch when the source triple is absent
//   - update-add:   as update, but adds NewValue when the source is absent
//   - add-if-absent: add Values only when (Key, Relation) holds nothing
//     (the compare-and-set used for atomic path creation)
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
// replace a value); the whole batch shares one durability Sync at the end.
//
// A batch IS atomic: the server validates every op before applying the first
// and runs the whole list under one lock, so a failing op (an update whose
// source triple is missing, an unknown kind) leaves the collection untouched.
// See tiedb.Apply.
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

// AddIfAbsent appends a compare-and-set op: values are written only when
// (key, relation) holds nothing at the moment the batch runs. This is what
// makes "create this path once" race-free without a separate query round-trip.
func (batch *Batch) AddIfAbsent(key, relation string, values []string) {
	batch.Ops = append(batch.Ops, BatchOp{Op: BatchAddIfAbsent, Key: key, Relation: relation, Values: values})
}

func (request *BatchRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := BatchReply{}
	if request.Batch == nil {
		reply.Success = false
		reply.Message = "batch: no batch in request"
		return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, errors.New(reply.Message)
	}
	col, err := env.Collection(request.Batch.Collection.Namespace, request.Batch.Collection.CollectionId)
	if err != nil {
		return ws.Reply{}, err
	}

	// Apply runs the whole batch under one changeMutex hold, validating every
	// op before the first is applied: a failing op leaves nothing behind.
	ops := make([]tiedb.Op, len(request.Batch.Ops))
	for i, op := range request.Batch.Ops {
		kind := op.Op
		if kind == BatchUpdate && op.AddOnFailure {
			kind = tiedb.OpUpdateAdd
		}
		ops[i] = tiedb.Op{
			Kind:     kind,
			Key:      op.Key,
			Relation: op.Relation,
			Values:   op.Values,
			NewValue: op.NewValue,
		}
	}
	if _, err := col.Apply(ops); err != nil {
		reply.Success = false
		reply.Message = err.Error()
		return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, errors.New(reply.Message)
	}

	col.Sync()
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
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
