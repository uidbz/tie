package api

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Drop to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

const (
	IdDrop = "Drop"
)

type DropRequest struct {
	ws.Request
	CollectionInfo
}

type DropReply struct {
	ws.ReplyStatus
}

func (request *DropRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := DropReply{}

	if err := env.Webservice.DropCollection(request.Namespace, request.CollectionId); err != nil {
		reply.Message = err.Error()
	} else {
		reply.Success = true
	}

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *DropRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewDropRequest()
}

func (c CollectionInfo) NewDropRequest() *DropRequest {
	request := &DropRequest{}
	request.Id = IdDrop
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &DropReply{}

	return request
}
