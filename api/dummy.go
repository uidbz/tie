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

func dummyHandler(env *ws.Environment, request *DummyRequest) (*DummyReply, error) {
	reply := DummyReply{}
	reply.Hello = "Hello, I got your data: " + request.SomeData
	reply.Success = true

	return &reply, nil
}

func (c CollectionInfo) NewDummyRequest() *DummyRequest {
	request := &DummyRequest{}
	request.Id = IdDummy
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId

	return request
}
