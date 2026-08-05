package api

import (
	"sort"

	"git.sr.ht/~uid/tie/tiedb"
	ws "git.sr.ht/~uid/tie/webservice"
)

const (
	IdCoTags = "CoTags"
)

// CoTagsRequest asks for all unique tags carried by entries that match ALL of
// Terms (AND) and NONE of Exclude, optionally scoped to a tie-type value.
// This is the "faceted refinement" query: given the user's current tag filter,
// it returns the set of tags that can be used to further narrow the results.
//
// Example: Terms=["tree","nature"] returns every tag found on files that carry
// both "tree" and "nature", e.g. ["2026","norway","sunset"].
type CoTagsRequest struct {
	ws.Request
	CollectionInfo

	Terms   []string `json:"terms"`
	Exclude []string `json:"exclude"`
	Scope   string   `json:"scope"`
}

// CoTagsReply holds the sorted, deduplicated tag names that co-occur with the
// query. Tags is nil (not empty) when the query matched nothing.
type CoTagsReply struct {
	ws.ReplyStatus
	Tags []string `json:"tags"`
}

func (request *CoTagsRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := CoTagsReply{}
	if len(request.Terms) > 0 {
		reply.OrigKey = request.Terms[0]
	}

	col := env.Collection(request.Namespace, request.CollectionId)

	q := tiedb.TagQuery{
		Include: request.Terms,
		Exclude: request.Exclude,
		Scope:   request.Scope,
		Reverse: true, // tag queries always use the reverse index
	}
	tags, found := col.CoTags(q, "tag")
	if !found {
		reply.Success = false
		reply.Message = "Key has no associated values"
		return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
	}

	sort.Strings(tags)
	reply.Tags = tags
	reply.Success = true

	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *CoTagsRequest) New() ws.RequestInterface {
	return CollectionInfo{}.NewCoTagsRequest(nil, nil, "")
}

func (c CollectionInfo) NewCoTagsRequest(terms, exclude []string, scope string) *CoTagsRequest {
	request := &CoTagsRequest{}
	request.Id = IdCoTags
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &CoTagsReply{}

	request.Terms = terms
	request.Exclude = exclude
	request.Scope = scope

	return request
}
