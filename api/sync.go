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

func syncHandler(env *ws.Environment, request *SyncRequest) (*SyncReply, error) {
	reply := SyncReply{}
	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}
	col.Sync()
	reply.Success = true

	return &reply, nil
}

func (c CollectionInfo) NewSyncRequest() *SyncRequest {
	request := &SyncRequest{}
	request.Id = IdSync
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId

	return request
}
