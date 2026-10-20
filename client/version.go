package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/version"
)

// ServerVersion asks the bound triplestore which build it is running.
func (tc *TieClient) ServerVersion() (version.Info, error) {
	reply, err := call[*api.VersionRequest, api.VersionReply](tc, api.NewVersionRequest())
	if err != nil {
		return version.Info{}, err
	}
	if err := replyError(reply.ReplyStatus); err != nil {
		return version.Info{}, err
	}
	return reply.Build, nil
}

// TripleStoreVersion asks a triplestore which build it is running, over the
// same GET /-/version the filehost answers. Unlike ServerVersion it needs no
// authenticated RPC round trip, so it works as a health probe from a browser or
// a monitoring check. An empty URL selects the client's own triplestore when
// called as a method-style helper on a scoped client is not needed: pass the
// address you want checked.
func TripleStoreVersion(url string) (version.Info, error) {
	resp, err := http.Get(strings.TrimRight(url, "/") + "/-/version")
	if err != nil {
		return version.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return version.Info{}, fmt.Errorf("triplestore returned %s (older triplestores have no /-/version endpoint)", resp.Status)
	}
	var body struct {
		Build version.Info `json:"build"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return version.Info{}, err
	}
	return body.Build, nil
}

// FileHostVersion asks a filehost which build it is running (GET /-/version).
func FileHostVersion(host FileHost) (version.Info, error) {
	return FileHostClient(host).Version()
}
