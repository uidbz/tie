package api

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

/*
To create a new request type:
1. Copy this file and rename to 'newname'
2. Rename all instances of Update to 'NewName'
3. Implement Reply and define fields in structs
4. Update to request slice and pass to NewWebservice on server
*/

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

func (request *UpdateRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := UpdateReply{}

	col := env.Collection(request.Namespace, request.CollectionId)

	if request.AddOnFailure {
		reply.Message, reply.Success = col.UpdateAdd(request.Key, request.Value1, request.Value2, request.NewValue2)
	} else {
		reply.Message, reply.Success = col.Update(request.Key, request.Value1, request.Value2, request.NewValue2)
	}

	reply.OrigKey = request.Key
	reply.OrigValue1 = request.Value2
	reply.OrigValue2 = request.Value2
	reply.OrigNewValue2 = request.NewValue2

	return ws.Reply{request.Id, reply}, nil
}

func (c CollectionInfo) NewUpdateRequest(update Update) *UpdateRequest {
	request := &UpdateRequest{}
	request.Id = IdUpdate
	request.Namespace = c.Namespace
	request.CollectionId = c.CollectionId
	request.ReplyStructPtr = &UpdateReply{}
	request.Key = update.Key
	request.Value1 = update.Value1
	request.Value2 = update.Value2
	request.NewValue2 = update.NewValue2
	request.AddOnFailure = update.AddOnFailure

	return request
}
