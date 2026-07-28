package api

import (
	"encoding/json"
	"io"

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

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

// StreamReply writes every forward triple as NDJSON (one JSON StringTriple per
// line) straight to w, so the daemon never buffers the whole collection. It
// drops the ReplyStatus envelope the buffered Reply carries — Dump always
// succeeds, and auth/not-found failures happen before any body byte is written.
func (request *DumpRequest) StreamReply(env *ws.Environment, w io.Writer) error {
	enc := json.NewEncoder(w) // Encode appends '\n' => NDJSON
	col := env.Collection(request.Namespace, request.CollectionId)
	var encErr error
	col.ForEachTriple(func(t tiedb.StringTriple) {
		if encErr != nil {
			return
		}
		encErr = enc.Encode(t)
	})
	return encErr
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
