package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdSync = "Sync"
)

type SyncRequest struct {
	ws.Request
	CollectionInfo
}

type SyncReply struct {
	ws.ReplyStatus
}

func (request *SyncRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := SyncReply{}
	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return ws.Reply{}, err
	}
	col.Sync()
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *SyncRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewSyncRequest()
}

func (c CollectionInfo) NewSyncRequest() *SyncRequest {
	request := &SyncRequest{}
	request.Id = IdSync
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &SyncReply{}

	return request
}
