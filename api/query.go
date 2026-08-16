package api

import (
	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

const (
	IdQuery = "Query"
)

// QueryRequest selects entries by association membership and returns them as a
// flat, ordered list of Rows. Terms is the full AND-list (every match must be
// associated with all of them); there is no positional seed key. Exclude lists
// terms a match must NOT carry. Scope restricts matches to associates of that
// value under a different relation than the terms (e.g. a tie-type).
// MissingRelation keeps only matches that carry no triple under the named
// relation — the "has no tag" case Exclude cannot express (Exclude removes a
// specific value, not the presence of a relation). Filter filters-in on the
// relation (value1). Reverse selects matches by reverse association (the
// tag-query case). When Expand is set, each match's own forward attributes are
// attached to its Row instead of the matched triples.
type QueryRequest struct {
	ws.Request
	CollectionInfo

	Terms           []string          `json:"terms"`
	Exclude         []string          `json:"exclude"`
	Scope           string            `json:"scope"`
	MissingRelation string            `json:"missingRelation"`
	Filter          string            `json:"filter"`
	Reverse         bool              `json:"reverse"`
	Expand          bool              `json:"expand"`
	Sort            tiedb.SortOptions `json:"sort"`
}

// QueryReply is the single, ordered result view. Rows is paginated per the
// request's Sort; TotalCount is the number of matching keys before pagination.
type QueryReply struct {
	ws.ReplyStatus
	Rows       []tiedb.Row `json:"rows"`
	TotalCount int         `json:"totalCount"`
}

func (request *QueryRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := QueryReply{}
	if len(request.Terms) > 0 {
		reply.OrigKey = request.Terms[0]
	}

	col := env.Collection(request.Namespace, request.CollectionId)

	q := tiedb.TagQuery{
		Include:         request.Terms,
		Exclude:         request.Exclude,
		Scope:           request.Scope,
		MissingRelation: request.MissingRelation,
		Reverse:         request.Reverse,
		Filter:          request.Filter,
		Sort:            request.Sort,
	}
	_, sorted, total, found := col.QueryTags(q)
	if !found || len(sorted) == 0 {
		reply.Success = false
		reply.Message = "Key has no associated values"
		return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
	}

	reply.TotalCount = total
	if request.Expand {
		keys := make([]string, 0, len(sorted))
		seen := make(map[string]bool, len(sorted))
		for _, t := range sorted {
			if !seen[t.Key] {
				seen[t.Key] = true
				keys = append(keys, t.Key)
			}
		}
		reply.Rows = col.ExpandKeys(keys, "")
	} else {
		reply.Rows = tiedb.RowsFromSorted(sorted)
	}
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *QueryRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewQueryRequest()
}

func (c CollectionInfo) NewQueryRequest() *QueryRequest {
	request := &QueryRequest{}
	request.Id = IdQuery
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &QueryReply{}

	return request
}
