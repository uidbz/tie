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

	Key     string
	Options GetOptions
}

// Options for GetWith
// GetNextLevel: Fetch next level ('value2's from result are used as keys)
// NextLevelValue1s: Filter-in 'value1' results from next level
// Filter: Filter-in 'value1'
// Reverse: Get reverse associations
// OnlyReverse: Do not get non-reverse associations
type GetOptions struct {
	Intersect        []Transform
	Exclude          []Transform
	NextLevelValue1s []string
	Filter           string
	Reverse          bool
	SortOnNextLevel  bool
	Sort             tiedb.SortOptions
}

type Transform struct {
	Key     string
	Reverse bool
}

type GetReply struct {
	ws.ReplyStatus
	Result     tiedb.TripleSet
	TotalCount int
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

func transform(dest *tiedb.TieTree, col *tiedb.Collection, list []Transform, exclude bool) *tiedb.TieTree {
	if len(list) != 0 {
		for _, x := range list {
			var t *tiedb.TieTree
			var found bool
			if x.Reverse {
				t, found = col.GetReverseAssociations(x.Key)
			} else {
				t, found = col.GetAssociations(x.Key)
			}
			if found {
				if exclude {
					dest = dest.Exclude(t)
				} else {
					dest = dest.Intersect(t)
				}
			}
		}
	}
	return dest
}

func (request *GetRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := GetReply{}
	reply.OrigKey = request.Key

	col := env.Collection(request.Namespace, request.CollectionId)
	// o := tiedb.LookupOptions{
	// 	Value1Filter: request.Options.Filter,
	// 	Offset:       request.Options.Offset,
	// 	Limit:        request.Options.Limit,
	// }
	if request.Options.Reverse {
		if reverse, found := col.GetReverseAssociations(request.Key); found {
			reverse = transform(reverse, col, request.Options.Intersect, false)
			reverse = transform(reverse, col, request.Options.Exclude, true)
			reply.Result, reply.TotalCount = col.GetTripleSet(reverse, request.Options.Filter, request.Options.Sort)
		}
	} else {
		if direct, found := col.GetAssociations(request.Key); found {
			if request.Options.SortOnNextLevel {
				// trees := col.GetValue2Trees(assPtr, request.Options.Filter)
				// for _, t := range trees {
				// 	col.Sort(t, request.Options.Sort)

				// set2, _ := col.GetTripleSet(key, request.Options. , o)

				// for _, x := range request.Options.NextLevelValue1s {
				// 	o.Value1Filter = x
				// 	set2, _ := col.GetTripleSet(key, t, o)
				// 	set[x] = set2[x]
				// }
				// }
			} else {
				direct = transform(direct, col, request.Options.Intersect, false)
				direct = transform(direct, col, request.Options.Exclude, true)
				reply.Result, reply.TotalCount = col.GetTripleSet(direct, request.Options.Filter, request.Options.Sort)
			}
			// for key, t := range trees {
			// 	if request.Options.Sort.SortBy != "" {
			// reply.Result = set
		}
	}

	if len(reply.Result) != 0 {
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
