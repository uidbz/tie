package api

import (
	"encoding/json"
	"io"

	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
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

// dumpStreamHandler writes every forward triple as NDJSON (one JSON StringTriple
// per line) straight to w, so the server never buffers the whole collection. It
// carries no ReplyStatus envelope — Dump always succeeds, and auth/not-found
// failures happen before any body byte is written. The buffered variant this
// replaced was dead: the server always preferred streaming, and TieClient.Dump
// collects from the stream.
func dumpStreamHandler(env *ws.Environment, request *DumpRequest, w io.Writer) error {
	enc := json.NewEncoder(w) // Encode appends '\n' => NDJSON
	col, found, err := resolveReadCollection(env, request.Namespace, request.CollectionId)
	if err != nil {
		return err
	}
	if !found {
		return nil // absent collection dumps as an empty stream
	}
	var encErr error
	col.ForEachTriple(func(t tiedb.StringTriple) {
		if encErr != nil {
			return
		}
		encErr = enc.Encode(t)
	})
	return encErr
}

func (c CollectionInfo) NewDumpRequest() *DumpRequest {
	request := &DumpRequest{}
	request.Id = IdDump
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId

	return request
}
