package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdDelete = "Delete"
)

type DeleteRequest struct {
	ws.Request
	CollectionInfo

	Key    string
	Value1 string
	Value2 string
}

type DeleteReply struct {
	ws.ReplyStatus
	OrigValue1 string
	OrigValue2 string
}

func deleteHandler(env *ws.Environment, request *DeleteRequest) (*DeleteReply, error) {
	reply := DeleteReply{}

	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}

	reply.Message, reply.Success = col.Delete(request.Key, request.Value1, request.Value2)

	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value1
	reply.OrigValue2 = request.Value2

	return &reply, nil
}

func (c CollectionInfo) NewDeleteRequest(key, value1, value2 string) *DeleteRequest {
	request := &DeleteRequest{}
	request.Id = IdDelete
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Key = key
	request.Value1 = value1
	request.Value2 = value2

	return request
}
