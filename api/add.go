package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdAdd = "Add"
)

type AddRequest struct {
	ws.Request
	CollectionInfo

	Key    string
	Value1 string
	Value2 string
}

type AddReply struct {
	ws.ReplyStatus
	OrigValue1 string
	OrigValue2 string
}

func addHandler(env *ws.Environment, request *AddRequest) (*AddReply, error) {
	reply := AddReply{}

	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}

	if err := col.Add(request.Key, request.Value1, request.Value2); err != nil {
		reply.Success = false
		reply.Message = err.Error()
		return &reply, err
	}

	reply.Success = true
	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value1
	reply.OrigValue2 = request.Value2

	return &reply, nil
}

func (c CollectionInfo) NewAddRequest(key, value1, value2 string) *AddRequest {
	request := &AddRequest{}
	request.Id = IdAdd
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Key = key
	request.Value1 = value1
	request.Value2 = value2

	return request
}
