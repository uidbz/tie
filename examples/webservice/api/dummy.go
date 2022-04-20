package api

import (
	"git.sr.ht/~uid/tie/tiedb"
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
	SomeData string
}

type DummyReply struct {
	ws.ReplyStatus
	Hello string
}

func (request *DummyRequest) Reply(username string, account func(string) tiedb.Collection) (ws.Reply, error) {
	reply := DummyReply{}
	reply.Hello = "Hello, I got your data: " + request.SomeData
	reply.Success = true

	return ws.Reply{request.Id, reply}, nil
}

func NewDummyRequest() *DummyRequest {
	request := &DummyRequest{}
	request.Id = IdDummy
	request.ReplyStructPtr = &DummyReply{}

	return request
}
