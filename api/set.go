package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdSet = "Set"
)

// SetRequest makes (Key, Relation) hold exactly Values: it replaces the whole
// value set for that relation in one server-side op. This is the first-class
// "set this field" primitive that replaces the client Get -> Delete-each -> Add
// dance. An empty Values clears the relation.
type SetRequest struct {
	ws.Request
	CollectionInfo

	Key      string   `json:"key"`
	Relation string   `json:"relation"`
	Values   []string `json:"values"`
}

type SetReply struct {
	ws.ReplyStatus
}

func setHandler(env *ws.Environment, request *SetRequest) (*SetReply, error) {
	reply := SetReply{}

	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}
	col.SetValues(request.Key, request.Relation, request.Values)

	reply.Success = true
	reply.OrigKey = request.Key

	return &reply, nil
}

func (c CollectionInfo) NewSetRequest(key, relation string, values []string) *SetRequest {
	request := &SetRequest{}
	request.Id = IdSet
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Key = key
	request.Relation = relation
	request.Values = values

	return request
}
