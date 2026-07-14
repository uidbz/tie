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
	// Include lists additional tags the result must ALL be associated with
	// (AND, by reverse association), on top of the seed Key. Exclude lists tags
	// the result must NOT be associated with.
	Include         []string
	Exclude         []string
	Filter          string
	Reverse         bool
	GetNextLevel    bool
	NextLevelFilter string
	Sort            tiedb.SortOptions
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

func (request *GetRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := GetReply{}
	reply.OrigKey = request.Key

	col := env.Collection(request.Namespace, request.CollectionId)

	q := tiedb.TagQuery{
		Include: append([]string{request.Key}, request.Options.Include...),
		Exclude: request.Options.Exclude,
		Reverse: request.Options.Reverse,
		Filter:  request.Options.Filter,
		Sort:    request.Options.Sort,
	}
	result, sorted, total, found := col.QueryTags(q)
	if found {
		reply.Result, reply.SortedResult, reply.TotalCount = result, sorted, total
		if request.Options.GetNextLevel {
			reply.NextLevelResult = make(tiedb.TripleSet, 0)
			if request.Options.Reverse {
				for key := range reply.Result {
					if t, ok := col.GetAssociations(key); ok {
						set, _ := col.GetTripleSet(t, request.Options.NextLevelFilter, request.Options.Sort)
						reply.NextLevelResult[key] = set[key]
					}
				}
			} else {
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
