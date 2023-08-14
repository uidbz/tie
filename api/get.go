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

	Key string
	GetOptions
}

// Options for GetWith
// GetNextLevel: Fetch next level ('value2's from result are used as keys)
// NextLevelValue1s: Filter-in 'value1' results from next level
// Filter: Filter-in 'value1'
// Reverse: Get reverse associations
// OnlyReverse: Do not get non-reverse associations
type GetOptions struct {
	NextLevelValue1s []string
	Filter           string
	Reverse          bool
	OnlyReverse      bool
}

type GetReply struct {
	ws.ReplyStatus
	Result        tiedb.TripleSet
	ReverseResult tiedb.TripleSet
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
	reply.OrigKey = request.Key

	col := env.Collection(request.Namespace, request.CollectionId)

	if !request.OnlyReverse {
		if assPtr, found := col.GetAssociations(request.Key); found {
			set, trees := col.GetTripleSet(request.Key, request.Filter, assPtr)

			for key, t := range trees {
				for _, x := range request.NextLevelValue1s {
					set2, _ := col.GetTripleSet(key, x, t)
					set[x] = set2[x]
				}
			}
			reply.Result = set
		}
	}
	if request.Reverse || request.OnlyReverse {
		if reverse, found := col.GetReverseAssociations(request.Key); found {
			reply.ReverseResult, _ = col.GetTripleSet(request.Key, request.Filter, reverse)
		}
	}

	if len(reply.Result) != 0 || len(reply.ReverseResult) != 0 {
		reply.Success = true
	} else {
		reply.Success = false
		reply.Message = "Key has no associated values"
	}

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
