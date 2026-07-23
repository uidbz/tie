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
	Collection     CollectionInfo
	AddRequests    []*AddRequest
	GetRequests    []*GetRequest
	DeleteRequests []*DeleteRequest
	UpdateRequests []*UpdateRequest
}

type BatchReply struct {
	ws.ReplyStatus

	AddReplys    []AddReply
	GetReplys    []GetReply
	DeleteReplys []DeleteReply
	UpdateReplys []UpdateReply
}

func (batch *Batch) Add(key, value1, value2 string) {
	batch.AddRequests = append(batch.AddRequests, batch.Collection.NewAddRequest(key, value1, value2))
}

func (batch *Batch) Get(key string) {
	batch.GetRequests = append(batch.GetRequests, batch.Collection.NewGetRequest(key))
}

func (batch *Batch) Delete(key, value1, value2 string) {
	batch.DeleteRequests = append(batch.DeleteRequests, batch.Collection.NewDeleteRequest(key, value1, value2))
}

func (batch *Batch) Update(update Update) {
	batch.UpdateRequests = append(batch.UpdateRequests, batch.Collection.NewUpdateRequest(update))
}

func (request *BatchRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := BatchReply{}

	tmpNamespace, tmpCollectionId := "", ""
	syncOnCollectionChange := func(namespace, collectionId string) {
		if tmpNamespace == "" {
			tmpNamespace, tmpCollectionId = namespace, collectionId
		} else if namespace != tmpNamespace && collectionId != tmpCollectionId {
			env.Collection(namespace, collectionId).Sync()
			tmpNamespace, tmpCollectionId = namespace, collectionId
		}
	}
	for _, x := range request.Batch.UpdateRequests {
		r, err := x.Reply(env)
		if err == nil {
			reply.UpdateReplys = append(reply.UpdateReplys, r.ReplyStructPtr.(UpdateReply))
		} else {
			reply.Success = false
			reply.Message = "Error updating values"
			return ws.Reply{request.Id, reply}, errors.New("Error updating values")
		}
		syncOnCollectionChange(x.Namespace, x.CollectionId)
	}

	env.Collection(tmpNamespace, tmpCollectionId).Sync() // Sync before continue to next request type
	tmpNamespace, tmpCollectionId = "", ""

	for _, x := range request.Batch.DeleteRequests {
		r, err := x.Reply(env)
		if err == nil {
			reply.DeleteReplys = append(reply.DeleteReplys, r.ReplyStructPtr.(DeleteReply))
		} else {
			reply.Success = false
			reply.Message = "Error deleting values"
			return ws.Reply{request.Id, reply}, errors.New("Error deleting values")
		}
		syncOnCollectionChange(x.Namespace, x.CollectionId)
	}
	env.Collection(tmpNamespace, tmpCollectionId).Sync() // Sync before continue to next request type
	tmpNamespace, tmpCollectionId = "", ""

	for _, x := range request.Batch.AddRequests {
		r, err := x.Reply(env)
		if err == nil {
			reply.AddReplys = append(reply.AddReplys, r.ReplyStructPtr.(AddReply))
		} else {
			reply.Success = false
			reply.Message = "Error adding values"
			return ws.Reply{request.Id, reply}, errors.New("Error adding values")
		}
		syncOnCollectionChange(x.Namespace, x.CollectionId)
	}
	env.Collection(tmpNamespace, tmpCollectionId).Sync() // Sync before continue to next request type
	tmpNamespace, tmpCollectionId = "", ""

	for _, x := range request.Batch.GetRequests {
		r, err := x.Reply(env)
		if err == nil {
			reply.GetReplys = append(reply.GetReplys, r.ReplyStructPtr.(GetReply))
		} else {
			reply.Success = false
			reply.Message = "Error getting values"
			return ws.Reply{request.Id, reply}, errors.New("Error getting values")
		}
	}

	reply.Success = true

	return ws.Reply{request.Id, reply}, nil
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
