package api

import (
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

	Key              string
	NextLevelValue1s []string
	Filter           string
}

type GetReply struct {
	ws.ReplyStatus
	Result tiedb.TripleSet
}

// Returns (value of Value2, true) from Result, if the only key in the Result is the requested key, and if only 1 Value2 exists.
// In any other case it returns (empty string, false)
func (gr GetReply) OneValue2(value1 string) (string, bool) {
	if val1, ok := gr.OneKey(); ok {
		return val1[value1].One()
	}

	return "", false
}

// Returns (Value1, true) from Result, if the only key in the Result is the requested key.
// In any other case it returns (nil, false)
func (gr GetReply) OneKey() (tiedb.Value1, bool) {
	if gr.Success && len(gr.Result) == 1 {
		if val1, ok := gr.Result[gr.OrigKey]; ok {
			return val1, true
		}
	}

	return nil, false
}

func (request *GetRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := GetReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	if assPtr, found := col.GetAssociations(request.Key); found {
		// replySlice, errString := RequestToStringSlice(col, r.Value, r.Relation, assPtr)
		// var replySet *tiedb.TrippleSet
		// var nextLevelRelation, relation []string
		// for _, x := range request.NextLevelValues {
		// 	if len(x) >= 1 {
		// 		if x[0] == '+' {
		// 			nextLevelRelation = append(nextLevelRelation, strings.TrimPrefix(x, "+"))
		// 		} else {
		// 			relation = append(relation, x)
		// 		}
		// 	}
		// }
		// if len(relation) > 1 {
		// 	reply.Success = false
		// 	return ws.Reply{request.Id, reply}, errors.New("Error: Max filters = 1, maybe you ment to use 'tie filters' or with + in front.")
		// }

		set, trees := col.GetTripleSet(request.Key, request.Filter, assPtr)

		for val2, t := range trees {
			for _, x := range request.NextLevelValue1s {
				set2, _ := col.GetTripleSet(val2, x, t)
				set[x] = set2[x]
			}
		}
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
		if len(set) != 0 {
			reply.Success = true
		} else {
			reply.Success = false
			reply.Message = "Key has 0 associated values"
		}
		reply.OrigKey = request.Key

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
