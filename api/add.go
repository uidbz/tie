package api

import (
	ws "github.com/uidbz/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Add to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

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

	col := env.Collection(request.Namespace, request.CollectionId)

	col.Add(request.Key, request.Value1, request.Value2)

	reply.Success = true
	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value2
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
