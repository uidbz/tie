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

func (request *AddRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := AddReply{}

	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return ws.Reply{}, err
	}

	if err := col.Add(request.Key, request.Value1, request.Value2); err != nil {
		reply.Success = false
		reply.Message = err.Error()
		return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, err
	}

	reply.Success = true
	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value1
	reply.OrigValue2 = request.Value2

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *AddRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewAddRequest("", "", "")
}

func (c CollectionInfo) NewAddRequest(key, value1, value2 string) *AddRequest {
	request := &AddRequest{}
	request.Id = IdAdd
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &AddReply{}
	request.Key = key
	request.Value1 = value1
	request.Value2 = value2

	return request
}
