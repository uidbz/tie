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
// NextLevelFilter: Filter-in 'value1' results from next level
// Filter: Filter-in 'value1'
// Reverse: Get reverse associations

type GetOptions struct {
	Intersect       []Transform
	Exclude         []Transform
	Filter          string
	Reverse         bool
	GetNextLevel    bool
	NextLevelFilter string
	Sort            tiedb.SortOptions
}

type Transform struct {
	Key     string
	Reverse bool
}

type GetReply struct {
	ws.ReplyStatus
	Result tiedb.TripleSet
	// SortedResult is the same triples as Result but ordered by the request's
	// Sort options and paginated (Offset/Limit). Result is an unordered map and
	// loses that order; clients that need a stable, paged sequence read this.
	SortedResult    []tiedb.StringTriple
	NextLevelResult tiedb.TripleSet
	// TotalCount is the number of matches before Offset/Limit were applied.
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
	if request.Options.Reverse {
		if reverse, found := col.GetReverseAssociations(request.Key); found {
			reverse = transform(reverse, col, request.Options.Intersect, false)
			reverse = transform(reverse, col, request.Options.Exclude, true)
			reply.Result, reply.SortedResult, reply.TotalCount = col.GetPage(reverse, request.Options.Filter, request.Options.Sort)
			if request.Options.GetNextLevel {
				reply.NextLevelResult = make(tiedb.TripleSet, 0)
				for key := range reply.Result {
					if t, ok := col.GetAssociations(key); ok {
						set, _ := col.GetTripleSet(t, request.Options.NextLevelFilter, request.Options.Sort)
						reply.NextLevelResult[key] = set[key]
					}
				}
			}
		}
	} else {
		if direct, found := col.GetAssociations(request.Key); found {
			direct = transform(direct, col, request.Options.Intersect, false)
			direct = transform(direct, col, request.Options.Exclude, true)
			reply.Result, reply.SortedResult, reply.TotalCount = col.GetPage(direct, request.Options.Filter, request.Options.Sort)
			if request.Options.GetNextLevel {
				reply.NextLevelResult = make(tiedb.TripleSet, 0)
				trees := make(map[string]bool)
				reply.Result.ForEachValue2(func(_, _, val2 string) {
					if _, ok := trees[val2]; !ok { // only lookup not previously looked up
						trees[val2] = true
						if t, ok := col.GetAssociations(val2); ok {
							set, _ := col.GetTripleSet(t, request.Options.NextLevelFilter, request.Options.Sort)
							reply.NextLevelResult[val2] = set[val2]
						}
					}
				})
			}
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
