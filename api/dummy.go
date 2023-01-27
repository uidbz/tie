package api

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Dummy to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

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

	return ws.Reply{request.Id, reply}, nil
}

func (c CollectionInfo) NewDummyRequest() *DummyRequest {
	request := &DummyRequest{}
	request.Id = IdDummy
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &DummyReply{}

	return request
}
