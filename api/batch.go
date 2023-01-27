package api

import (
	"errors"

	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Batch to 'NewName'
3. Implement Reply and define fields in structs
4. Batch to request slice and pass to NewWebservice on server
*/

const (
	IdBatch = "Batch"
)

type BatchRequest struct {
	ws.Request
	Batch *Batch
}

type Batch struct {
	Add    []*AddRequest
	Get    []*GetRequest
	Delete []*DeleteRequest
	Update []*UpdateRequest
}

type BatchReply struct {
	ws.ReplyStatus

	Add    []AddReply
	Get    []GetReply
	Delete []DeleteReply
	Update []UpdateReply
}

func (request *BatchRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := BatchReply{}

	for _, x := range request.Batch.Update {
		r, err := x.Reply(env)
		if err == nil {
			reply.Update = append(reply.Update, r.ReplyStructPtr.(UpdateReply))
		} else {
			reply.Success = false
			reply.Message = "Error updating values"
			return ws.Reply{request.Id, reply}, errors.New("Error updating values")
		}
	}

	for _, x := range request.Batch.Delete {
		r, err := x.Reply(env)
		if err == nil {
			reply.Delete = append(reply.Delete, r.ReplyStructPtr.(DeleteReply))
		} else {
			reply.Success = false
			reply.Message = "Error deleting values"
			return ws.Reply{request.Id, reply}, errors.New("Error deleting values")
		}
	}

	for _, x := range request.Batch.Add {
		r, err := x.Reply(env)
		if err == nil {
			reply.Add = append(reply.Add, r.ReplyStructPtr.(AddReply))
		} else {
			reply.Success = false
			reply.Message = "Error adding values"
			return ws.Reply{request.Id, reply}, errors.New("Error adding values")
		}
	}

	for _, x := range request.Batch.Get {
		r, err := x.Reply(env)
		if err == nil {
			reply.Get = append(reply.Get, r.ReplyStructPtr.(GetReply))
		} else {
			reply.Success = false
			reply.Message = "Error getting values"
			return ws.Reply{request.Id, reply}, errors.New("Error getting values")
		}
	}

	reply.Success = true

	return ws.Reply{request.Id, reply}, nil
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
