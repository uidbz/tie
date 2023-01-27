package api

import (
	"errors"
	"strings"

	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Get to 'NewName'
3. Implement Reply and define fields in structs
4. Add to request slice and pass to NewWebservice on server
*/

const (
	IdGet = "Get"
)

type GetRequest struct {
	ws.Request
	CollectionInfo

	Key             string
	NextLevelValues []string
	Filter          string
}

type GetReply struct {
	ws.ReplyStatus
	Result tiedb.TripleSet
}

func (request *GetRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := GetReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	found, assPtr := col.GetAssociations(request.Key)

	if found {
		// replySlice, errString := RequestToStringSlice(col, r.Value, r.Relation, assPtr)
		// var replySet *tiedb.TrippleSet
		var nextLevelRelation, relation []string
		for _, x := range request.NextLevelValues {
			if len(x) >= 1 {
				if x[0] == '+' {
					nextLevelRelation = append(nextLevelRelation, strings.TrimPrefix(x, "+"))
				} else {
					relation = append(relation, x)
				}
			}
		}
		if len(relation) > 1 {
			reply.Success = false
			return ws.Reply{request.Id, reply}, errors.New("Error: Max filters = 1, maybe you ment to use 'tie filters' or with + in front.")
		}

		set, _ := col.SetToString(request.Key, request.Filter, assPtr)
		// replySlice = append(replySlice, set)
		// if len(nextLevelRelation) > 0 {
		// 	for i, x := range trees {
		// 		for _, rel := range nextLevelRelation {
		// 			set2, _ := col.SetToString2(set.Value2[i], rel, x)
		// 			replySlice = append(replySlice, set2)
		// 		}
		// 	}
		// }
		// set.ForEach(func(key, val1, val2 string) {
		// 	fmt.Println(key, val1, val2)
		// })
		reply.Result = set
		reply.Success = true

		return ws.Reply{request.Id, reply}, nil
	}
	reply.Success = false

	return ws.Reply{request.Id, reply}, nil
}

func (c CollectionInfo) NewGetRequest(key string) *GetRequest {
	request := &GetRequest{}
	request.Id = IdGet
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &GetReply{}

	request.Key = key

	return request
}
