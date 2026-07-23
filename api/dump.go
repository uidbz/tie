package api

import (
	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

const (
	IdDump = "Dump"
)

type DumpRequest struct {
	ws.Request
	CollectionInfo
}

type DumpReply struct {
	ws.ReplyStatus
	Triples []tiedb.StringTriple
}

func (request *DumpRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := DumpReply{}

	col := env.Collection(request.Namespace, request.CollectionId)
	col.ForEachTriple(func(t tiedb.StringTriple) {
		reply.Triples = append(reply.Triples, t)
	})
	reply.Success = true

	return ws.Reply{request.Id, reply}, nil
}

func (request *DumpRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewDumpRequest()
}

func (c CollectionInfo) NewDumpRequest() *DumpRequest {
	request := &DumpRequest{}
	request.Id = IdDump
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &DumpReply{}

	return request
}
