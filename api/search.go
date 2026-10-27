package api

import (
	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdSearch = "Search"
)

// SearchRequest runs a free-text search over the collection's in-memory index.
// Relations names the searchable predicates; TieTypes optionally restricts
// candidates by tie-type (narrowed through the reverse tie-type index when it
// is available). Mode selects words/substring/prefix/fuzzy matching. Weights
// optionally scales a relation's contribution in percent.
//
// Search is a read: it changes nothing and is allowed for a read-role caller
// and a read-only database. An empty query or an empty relation list is a
// successful empty result, not an error.
type SearchRequest struct {
	ws.Request
	CollectionInfo

	Query     string           `json:"query"`
	Relations []string         `json:"relations"`
	TieTypes  []string         `json:"tieTypes"`
	Mode      tiedb.SearchMode `json:"mode"`
	Weights   map[string]int   `json:"weights,omitempty"`
	Offset    int              `json:"offset"`
	Limit     int              `json:"limit"`
	// Expand attaches each match's full forward attributes to its row, saving
	// a second round trip for callers that need to render or group results.
	Expand bool `json:"expand"`
}

// SearchReply is the ranked page. TotalCount is the number of matching
// subjects before paging; Revision is the index generation the page was
// derived from (a client paging across writes can restart when it changes).
type SearchReply struct {
	ws.ReplyStatus
	Matches    []tiedb.SearchMatch `json:"matches"`
	TotalCount int                 `json:"totalCount"`
	Revision   uint64              `json:"revision"`
}

func searchHandler(env *ws.Environment, request *SearchRequest) (*SearchReply, error) {
	reply := SearchReply{Success: true}

	col, found, err := resolveReadCollection(env, request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}
	if !found {
		return &reply, nil
	}

	matches, total, revision := col.Search(tiedb.SearchQuery{
		Text:      request.Query,
		Relations: request.Relations,
		TieTypes:  request.TieTypes,
		Mode:      request.Mode,
		Offset:    request.Offset,
		Limit:     request.Limit,
		Weights:   request.Weights,
	})
	reply.Matches = matches
	reply.TotalCount = total
	reply.Revision = revision

	if request.Expand && len(matches) > 0 {
		keys := make([]string, 0, len(matches))
		for _, m := range matches {
			keys = append(keys, m.Key)
		}
		attrs := make(map[string]map[string][]string, len(matches))
		for _, row := range col.ExpandKeys(keys, "") {
			attrs[row.Key] = row.Attributes
		}
		for i := range reply.Matches {
			reply.Matches[i].Attributes = attrs[reply.Matches[i].Key]
		}
	}
	return &reply, nil
}

func (c CollectionInfo) NewSearchRequest() *SearchRequest {
	request := &SearchRequest{}
	request.Id = IdSearch
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	return request
}
