package api

import (
	tiedb "github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdMkDir = "MkDir"
)

// MkDirRequest asks the server to create a directory node for a virtual path,
// atomically. The store does not know tie's vocabulary, so the request names
// the relations the node should carry; what the store guarantees is that the
// "does this path already have a UID" check and the writes happen together,
// under one changeMutex hold.
//
// NewUID is minted by the caller and used only if the path is free. The reply
// carries the UID the path actually has, which is the other creator's when a
// concurrent request won the race — so the caller never has to guess, and never
// ends up with two nodes for one path.
type MkDirRequest struct {
	ws.Request
	CollectionInfo
	Spec tiedb.DirSpec `json:"spec"`
}

type MkDirReply struct {
	ws.ReplyStatus
	// UID is the node that owns the path after the request.
	UID string `json:"uid"`
	// Existed says the path was already owned, so nothing was written.
	Existed bool `json:"existed"`
}

func mkDirHandler(env *ws.Environment, request *MkDirRequest) (*MkDirReply, error) {
	reply := MkDirReply{}
	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return &reply, err
	}

	uid, existed, err := col.MkDir(request.Spec)
	if err != nil {
		reply.Message = err.Error()
		return &reply, err
	}
	reply.Success = true
	reply.UID = uid
	reply.Existed = existed

	return &reply, nil
}

func (c CollectionInfo) NewMkDirRequest(spec tiedb.DirSpec) *MkDirRequest {
	request := &MkDirRequest{}
	request.Id = IdMkDir
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Spec = spec

	return request
}
