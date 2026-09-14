package api

import (
	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdCheckIndex = "CheckIndex"
)

// CheckIndexRequest cross-checks a collection's forward and reverse association
// indexes server-side (tiedb.Collection.CheckIndex). Deep also validates every
// forward position against its on-disk record; Repair fixes the divergences in
// memory. It is classified as a write request (even without Repair) so that
// only write-role users can run it: it blocks the collection's writers for the
// duration of the walk.
type CheckIndexRequest struct {
	ws.Request
	CollectionInfo
	Deep        bool
	Repair      bool
	SampleLimit int
}

type CheckIndexReply struct {
	ws.ReplyStatus
	Report tiedb.IndexReport
}

func (request *CheckIndexRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := CheckIndexReply{}
	col := env.Collection(request.Namespace, request.CollectionId)
	reply.Report = col.CheckIndex(tiedb.IndexCheckOptions{
		Deep:        request.Deep,
		Repair:      request.Repair,
		SampleLimit: request.SampleLimit,
	})
	reply.Success = true
	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *CheckIndexRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewCheckIndexRequest(false, false)
}

func (c CollectionInfo) NewCheckIndexRequest(deep, repair bool) *CheckIndexRequest {
	request := &CheckIndexRequest{}
	request.Id = IdCheckIndex
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Deep = deep
	request.Repair = repair
	request.ReplyStructPtr = &CheckIndexReply{}

	return request
}
