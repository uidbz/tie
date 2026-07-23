package api

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Sync to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

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
	env.Collection(request.Namespace, request.CollectionId).Sync()
	reply.Success = true

	return ws.Reply{request.Id, reply}, nil
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
