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

func versionHandler(env *ws.Environment, request *VersionRequest) (*VersionReply, error) {
	reply := VersionReply{Server: "tie-triplestore", Build: version.Get()}
	reply.Success = true
	return &reply, nil
}

func NewVersionRequest() *VersionRequest {
	request := &VersionRequest{}
	request.Id = IdVersion
	return request
}
