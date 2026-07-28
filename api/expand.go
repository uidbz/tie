package api

import (
	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

const (
	IdExpand = "Expand"
)

// ExpandRequest fetches the forward attributes of many keys in one round trip,
// returning one Row per key that exists. It collapses the N+1 pattern where a
// caller lists keys and then issues one Get per key to fetch their metadata.
// Filter filters-in on the relation (value1); empty means all relations.
type ExpandRequest struct {
	ws.Request
	CollectionInfo

	Keys   []string `json:"keys"`
	Filter string   `json:"filter"`
}

type ExpandReply struct {
	ws.ReplyStatus
	Rows []tiedb.Row `json:"rows"`
}

func (request *ExpandRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := ExpandReply{}

	col := env.Collection(request.Namespace, request.CollectionId)
	reply.Rows = col.ExpandKeys(request.Keys, request.Filter)
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *ExpandRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewExpandRequest(nil, "")
}

func (c CollectionInfo) NewExpandRequest(keys []string, filter string) *ExpandRequest {
	request := &ExpandRequest{}
	request.Id = IdExpand
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &ExpandReply{}
	request.Keys = keys
	request.Filter = filter

	return request
}
