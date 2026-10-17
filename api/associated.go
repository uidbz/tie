package api

import (
	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdAssociated = "Associated"
)

type AssociatedRequest struct {
	ws.Request
	CollectionInfo

	Key string
	AssociatedOptions
}

type AssociatedOptions struct {
	MatchValue1 string
}

type AssociatedReply struct {
	ws.ReplyStatus
	Result tiedb.TripleSet
}

func associatedHandler(env *ws.Environment, request *AssociatedRequest) (*AssociatedReply, error) {
	reply := AssociatedReply{}

	col, found, err := resolveReadCollection(env, request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}

	var assPtr *tiedb.AssociationSet
	if found {
		assPtr, found = col.GetReverseAssociations(request.Key)
	}
	if found {
		set, _ := col.GetTripleSet(assPtr, "", tiedb.SortOptions{Limit: -1}) // unlimited: Associated has no limit parameter
		set.ForEachKey(func(key string) {
			if t, ok := col.GetAssociations(key); ok {
				set2, _ := col.GetTripleSet(t, request.MatchValue1, tiedb.SortOptions{Limit: -1}) // unlimited: Associated has no limit parameter
				set[key] = set2[key]
			}
		})
		reply.Result = set
		reply.Success = true
		reply.OrigKey = request.Key

		return &reply, nil
	}
	reply.Success = false

	return &reply, nil
}

func (c CollectionInfo) NewAssociatedRequest(key string) *AssociatedRequest {
	request := &AssociatedRequest{}
	request.Id = IdAssociated
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId

	request.Key = key

	return request
}
