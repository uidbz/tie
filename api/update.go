package api

import (
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdUpdate = "Update"
)

type UpdateRequest struct {
	ws.Request
	CollectionInfo
	Update
}

type Update struct {
	Key          string
	Value1       string
	Value2       string
	NewValue2    string
	AddOnFailure bool
}

type UpdateReply struct {
	ws.ReplyStatus
	OrigValue1    string
	OrigValue2    string
	OrigNewValue2 string
}

func updateHandler(env *ws.Environment, request *UpdateRequest) (*UpdateReply, error) {
	reply := UpdateReply{}

	col, err := env.Collection(request.Namespace, request.CollectionId)
	if err != nil {
		return nil, err
	}

	if request.AddOnFailure {
		reply.Message, reply.Success = col.UpdateAdd(request.Key, request.Value1, request.Value2, request.NewValue2)
	} else {
		reply.Message, reply.Success = col.Update(request.Key, request.Value1, request.Value2, request.NewValue2)
	}

	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value1
	reply.OrigValue2 = request.Value2
	reply.OrigNewValue2 = request.NewValue2

	return &reply, nil
}

func (c CollectionInfo) NewUpdateRequest(update Update) *UpdateRequest {
	request := &UpdateRequest{}
	request.Id = IdUpdate
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.Key = update.Key
	request.Value1 = update.Value1
	request.Value2 = update.Value2
	request.NewValue2 = update.NewValue2
	request.AddOnFailure = update.AddOnFailure

	return request
}
