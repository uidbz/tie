package api

import (
	ws "github.com/uidbz/tie/webservice"
)

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
