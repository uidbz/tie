package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdDummy = "Dummy"
)

type DummyRequest struct {
	ws.Request
	CollectionInfo

	SomeData string
}

type DummyReply struct {
	ws.ReplyStatus
	Hello string
}

func (request *DummyRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := DummyReply{}
	reply.Hello = "Hello, I got your data: " + request.SomeData
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *DummyRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewDummyRequest()
}

func (c CollectionInfo) NewDummyRequest() *DummyRequest {
	request := &DummyRequest{}
	request.Id = IdDummy
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &DummyReply{}

	return request
}
