package api

import (
	"github.com/uidbz/tie/version"
	ws "github.com/uidbz/tie/webservice"
)

const (
	IdVersion = "Version"
)

// VersionRequest asks the triplestore which build it is running. It touches no
// collection and is a read request.
type VersionRequest struct {
	ws.Request
}

type VersionReply struct {
	ws.ReplyStatus
	Server string       `json:"server"` // binary name, "tie-triplestore"
	Build  version.Info `json:"build"`
}

func (request *VersionRequest) Reply(env *ws.Environment) (ws.Reply, error) {
	reply := VersionReply{Server: "tie-triplestore", Build: version.Get()}
	reply.Success = true
	return ws.Reply{RequestName: request.Id, ReplyStructPtr: reply}, nil
}

func (request *VersionRequest) New() ws.RequestInterface {
	return NewVersionRequest()
}

func NewVersionRequest() *VersionRequest {
	request := &VersionRequest{}
	request.Id = IdVersion
	request.ReplyStructPtr = &VersionReply{}
	return request
}
