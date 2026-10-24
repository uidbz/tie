package api

import (
	"encoding/json"

	"github.com/uidbz/tie/auth"
	ws "github.com/uidbz/tie/webservice"
)

// RegisterAll installs the tie protocol on a webservice: every request type,
// its handler, and the access it needs. A server that serves tie is
//
//	ws := webservice.NewWebservice(config)
//	api.RegisterAll(ws)
//
// The access level is stated where the request is registered, so a new request
// cannot fall out of a separate read/write table and be treated as a write by
// accident — or, worse, as a read (§7.5). Reads may be served to a read-only
// principal; writes may create a collection.
func RegisterAll(srv *ws.Webservice) {
	// Reads.
	ws.Register(srv, IdDummy, auth.AccessRead, dummyHandler)
	ws.Register(srv, IdQuery, auth.AccessRead, queryHandler)
	ws.Register(srv, IdExpand, auth.AccessRead, expandHandler)
	ws.Register(srv, IdAssociated, auth.AccessRead, associatedHandler)
	ws.Register(srv, IdCoTags, auth.AccessRead, coTagsHandler)
	ws.Register(srv, IdVersion, auth.AccessRead, versionHandler)
	// Dump answers as NDJSON so a large collection never sits in memory whole.
	ws.RegisterStream(srv, IdDump, auth.AccessRead, dumpStreamHandler)

	// Writes.
	ws.Register(srv, IdAdd, auth.AccessWrite, addHandler)
	ws.Register(srv, IdSet, auth.AccessWrite, setHandler)
	ws.Register(srv, IdDelete, auth.AccessWrite, deleteHandler)
	ws.Register(srv, IdUpdate, auth.AccessWrite, updateHandler)
	ws.Register(srv, IdBatch, auth.AccessWrite, batchHandler)
	ws.Register(srv, IdMkDir, auth.AccessWrite, mkDirHandler)
	ws.Register(srv, IdDrop, auth.AccessWrite, dropHandler)
	ws.Register(srv, IdSync, auth.AccessWrite, syncHandler)
	ws.Register(srv, IdCheckIndex, auth.AccessWrite, checkIndexHandler)
	// CheckIndex is classified write because it blocks the collection's writers,
	// but the diagnostic pass (no repair) is safe on a read-only snapshot. A
	// repair request is still refused.
	srv.SetReadOnlyGuard(IdCheckIndex, func(raw []byte) bool {
		var req CheckIndexRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return false
		}
		return !req.Repair
	})
}
