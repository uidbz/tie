package api

import (
	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
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
}

func (request *AddRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := AddReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	a := col.Add(request.Key, request.Value1, request.Value2)
	if a != nil {
		reply.Success = true
		col.Add(request.Value2, tiedb.ASSOCIATED, request.Key)
	} else {
		reply.Success = false
		reply.Message = "Something went wrong (disk full?)"
	}

	return ws.Reply{request.Id, reply}, nil
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
