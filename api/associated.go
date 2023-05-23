package api

import (
	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Associated to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

const (
	IdAssociated = "Associated"
)

type AssociatedRequest struct {
	ws.Request
	CollectionInfo

	Key string
}

type AssociatedReply struct {
	ws.ReplyStatus
	Result tiedb.TripleSet
}

func (request *AssociatedRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := AssociatedReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	if assPtr, found := col.GetAssociations(request.Key); found {
		_, trees := col.GetTripleSet(request.Key, tiedb.ASSOCIATED, assPtr)

		set := make(tiedb.TripleSet)
		for val2, t := range trees {
			set2, _ := col.GetTripleSet(val2, "", t)
			set[val2] = set2[val2]
		}
		reply.Result = set
		reply.Success = true
		reply.OrigKey = request.Key

		return ws.Reply{request.Id, reply}, nil
	}
	reply.Success = false

	return ws.Reply{request.Id, reply}, nil
}

func (c CollectionInfo) NewAssociatedRequest(key string) *AssociatedRequest {
	request := &AssociatedRequest{}
	request.Id = IdAssociated
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &AssociatedReply{}

	request.Key = key

	return request
}
