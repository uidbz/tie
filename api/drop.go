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

func dropHandler(env *ws.Environment, request *DropRequest) (*DropReply, error) {
	reply := DropReply{}

	if err := env.Webservice.DropCollection(request.Namespace, request.CollectionId); err != nil {
		reply.Message = err.Error()
	} else {
		reply.Success = true
	}

	return &reply, nil
}

func (c CollectionInfo) NewDropRequest() *DropRequest {
	request := &DropRequest{}
	request.Id = IdDrop
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId

	return request
}
