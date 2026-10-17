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
	// Result is one Row per subject associated with the requested key, holding
	// that subject's forward attributes filtered by MatchValue1. It used to be a
	// tiedb.TripleSet — nested maps keyed by subject, relation and value, which
	// made the JSON reply order-nondeterministic and cost a map allocation per
	// value (§8 S9). Rows are the shape every other read returns.
	Rows []tiedb.Row `json:"rows"`
}

// associatedHandler answers "what is associated with this key": the subjects
// with a reverse edge to Key, each expanded with its own attributes under
// MatchValue1. That is exactly Query{Terms: [Key], Reverse: true, Filter:
// MatchValue1, Expand: true}, and new callers should use Query; Associated stays
// for clients already speaking it.
func associatedHandler(env *ws.Environment, request *AssociatedRequest) (*AssociatedReply, error) {
	reply := AssociatedReply{}

	col, found, err := resolveReadCollection(env, request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}

	var subjects []string
	if found {
		if set, ok := col.GetReverseAssociations(request.Key); ok {
			// Unlimited: Associated has no limit parameter.
			sorted, _ := col.GetPage(set, "", tiedb.SortOptions{Limit: -1})
			seen := make(map[string]bool, len(sorted))
			for _, t := range sorted {
				if !seen[t.Key] {
					seen[t.Key] = true
					subjects = append(subjects, t.Key)
				}
			}
		}
	}
	if len(subjects) == 0 {
		reply.Success = false
		return &reply, nil
	}

	reply.Rows = col.ExpandKeys(subjects, request.MatchValue1)
	reply.Success = true
	reply.OrigKey = request.Key

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
