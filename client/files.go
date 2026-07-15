package client

import (
	"errors"

	"git.sr.ht/~uid/tie/io/getlib"
	"git.sr.ht/~uid/tie/io/putlib"
)

// ResolveHost looks up a filehost by name. An empty name selects the first
// entry in DefaultFileHosts.
func (tc *TieClient) ResolveHost(name string) (FileHost, error) {
	if name == "" {
		if len(tc.Config.DefaultFileHosts) == 0 {
			return FileHost{}, errors.New("no filehost configured: set DefaultFileHosts or pass a host name")
		}
		name = tc.Config.DefaultFileHosts[0]
	}
	host, ok := tc.Config.FileHosts[name]
	if !ok {
		return FileHost{}, errors.New("unknown filehost '" + name + "'")
	}
	return host, nil
}

// UploadResult reports the outcome of an upload. It mirrors the transport-layer
// status with only FFI-friendly fields so bindings never touch putlib types.
type UploadResult struct {
	Items    []UploadedItem
	ErrorMsg string
}

// UploadedItem is a single stored file.
type UploadedItem struct {
	Hash      string
	Filename  string
	MediaType string
	Size      int
	ErrorMsg  string
}

// Upload stores file (or directory) on the named filehost and returns the
// resulting hashes. Pass an empty hostName to use the default filehost.
func (tc *TieClient) Upload(hostName, file string) (*UploadResult, error) {
	host, err := tc.ResolveHost(hostName)
	if err != nil {
		return nil, err
	}
	status := putlib.Upload(host.URL, file, putlib.PutConfig{Client: httpClientFor(host)})
	result := &UploadResult{ErrorMsg: status.ErrorMsg}
	for _, item := range status.UploadedItems {
		result.Items = append(result.Items, UploadedItem{
			Hash:      item.Hash,
			Filename:  item.Filename,
			MediaType: item.MediaType,
			Size:      item.Size,
			ErrorMsg:  item.ErrorMsg,
		})
	}
	return result, nil
}

// Download fetches sourceHash from the named filehost into dest. Pass an empty
// hostName to use the default filehost.
func (tc *TieClient) Download(hostName, sourceHash, dest string) error {
	host, err := tc.ResolveHost(hostName)
	if err != nil {
		return err
	}
	return getlib.DownloadFile(httpClientFor(host), host.URL, sourceHash, dest)
}
