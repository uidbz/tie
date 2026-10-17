package api

import (
	"github.com/uidbz/tie/auth"
	ws "github.com/uidbz/tie/webservice"
)

// A request type is its struct plus a handler function. To add one: define the
// request and reply structs, write the handler, and register it — the id, the
// access it needs and the handler go on the same line. The client side is
// webservice.Call[*MyRequest, MyReply](client, req). There is no prototype to
// keep in sync and no read/write table to remember to update.

const IdDummy = "Dummy"

type DummyRequest struct {
	ws.Request
	SomeData string
}

type DummyReply struct {
	ws.ReplyStatus
	Hello string
}

func dummyHandler(env *ws.Environment, request *DummyRequest) (*DummyReply, error) {
	reply := DummyReply{
		Hello:   "Hello, I got your data: " + request.SomeData,
		Success: true,
	}
	return &reply, nil
}

// NewDummyRequest builds a request. The id is what the server routes on; the
// reply type is named by the caller, not carried by the request.
func NewDummyRequest() *DummyRequest {
	request := &DummyRequest{}
	request.Id = IdDummy
	return request
}

// Register installs this example's request type on a webservice.
func Register(srv *ws.Webservice) {
	ws.Register(srv, IdDummy, auth.AccessRead, dummyHandler)
}
