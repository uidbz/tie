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
	reply, err := run[api.VersionReply](tc, api.NewVersionRequest())
	if err != nil {
		return version.Info{}, err
	}
	if err := replyError(reply.ReplyStatus); err != nil {
		return version.Info{}, err
	}
	return reply.Build, nil
}

// FileHostVersion asks a filehost which build it is running (GET /-/version).
func FileHostVersion(host FileHost) (version.Info, error) {
	hc := HTTPClientFor(host)
	resp, err := hc.Get(strings.TrimRight(host.URL, "/") + "/-/version")
	if err != nil {
		return version.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return version.Info{}, fmt.Errorf("filehost returned %s (older filehosts have no /-/version endpoint)", resp.Status)
	}
	var body struct {
		Build version.Info `json:"build"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return version.Info{}, err
	}
	return body.Build, nil
}
