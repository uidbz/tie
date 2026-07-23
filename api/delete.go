package api

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Delete to 'NewName'
3. Implement Reply and define fields in structs
4. Delete to request slice and pass to NewWebservice on server
*/

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

func (request *DeleteRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := DeleteReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	reply.Message, reply.Success = col.Delete(request.Key, request.Value1, request.Value2)

	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value2
	reply.OrigValue2 = request.Value2

	return ws.Reply{request.Id, reply}, nil
}

func (request *DeleteRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewDeleteRequest("", "", "")
}

func (c CollectionInfo) NewDeleteRequest(key, value1, value2 string) *DeleteRequest {
	request := &DeleteRequest{}
	request.Id = IdDelete
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &DeleteReply{}
	request.Key = key
	request.Value1 = value1
	request.Value2 = value2

	return request
}
