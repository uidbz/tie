package client

import (
	"errors"
	"io"

	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
)

// ResolveHost looks up a filehost by name. An empty name selects the first
// entry in DefaultFileHosts.
func (tc *TieClient) ResolveHost(name string) (FileHost, error) {
	if name == "" {
		if len(tc.active.FileHosts) == 0 {
			return FileHost{}, errors.New("no filehost configured: set the collection's FileHosts (or DefaultFileHosts) or pass a host name")
		}
		name = tc.active.FileHosts[0]
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

// UploadOptions carries the optional upload knobs. The zero value uploads with
// the store's default retention and no owner token, matching UploadTo.
type UploadOptions struct {
	// Retention is a Go duration (e.g. "72h") or "infinite". Empty means the
	// store's DefaultRetention applies, which may be finite — callers storing a
	// blob that a triple will keep referencing should pass "infinite" so the
	// filehost reaper cannot collect it out from under them.
	Retention string
	// OwnerToken, when non-empty, lets the uploader change the blob's retention
	// later. Only its hash is kept server-side.
	OwnerToken string
	// Progress, when non-nil, receives each chunk of uploaded bytes so callers
	// can render a progress bar. The total is the file (or manifest) size.
	Progress io.Writer
}

// Upload stores file (or directory) on the named filehost and returns the
// resulting hashes. Pass an empty hostName to use the default filehost.
func (tc *TieClient) Upload(hostName, file string) (*UploadResult, error) {
	return tc.UploadWithOptions(hostName, file, UploadOptions{})
}

// UploadWithOptions behaves like Upload but applies opts, letting callers set
// retention and an owner token.
func (tc *TieClient) UploadWithOptions(hostName, file string, opts UploadOptions) (*UploadResult, error) {
	host, err := tc.ResolveHost(hostName)
	if err != nil {
		return nil, err
	}
	return UploadToWithOptions(host, file, opts)
}

// UploadTo stores file (or directory) on an explicit filehost, bypassing config
// lookup. Useful for one-off targets (e.g. a raw --server address).
func UploadTo(host FileHost, file string) (*UploadResult, error) {
	return UploadToWithOptions(host, file, UploadOptions{})
}

// UploadToWithProgress behaves like UploadTo but, when progress is non-nil,
// writes each chunk of uploaded bytes to it so callers can render a progress
// bar. The total byte count is the file (or manifest) size.
func UploadToWithProgress(host FileHost, file string, progress io.Writer) (*UploadResult, error) {
	return UploadToWithOptions(host, file, UploadOptions{Progress: progress})
}

// UploadToWithOptions stores file (or directory) on an explicit filehost with
// full control over retention, owner token and progress reporting.
//
// A nil error does not mean the upload succeeded: the filehost answers 200 even
// when its recomputed checksum disagrees with the client's (it drops the blob
// but still reports a hash), so callers must check UploadResult.ErrorMsg and
// each item's ErrorMsg.
func UploadToWithOptions(host FileHost, file string, opts UploadOptions) (*UploadResult, error) {
	status := putlib.Upload(host.URL, file, putlib.PutConfig{
		Client:     HTTPClientFor(host),
		Progress:   opts.Progress,
		Store:      host.Store,
		Retention:  opts.Retention,
		OwnerToken: opts.OwnerToken,
	})
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
	return DownloadFrom(host, sourceHash, dest)
}

// DownloadFrom fetches sourceHash from an explicit filehost into dest,
// bypassing config lookup.
func DownloadFrom(host FileHost, sourceHash, dest string) error {
	return DownloadFromWithProgress(host, sourceHash, dest, nil)
}

// DownloadFromWithProgress behaves like DownloadFrom but, when progress is
// non-nil, writes each chunk of downloaded file bytes to it so callers can
// render a progress bar.
func DownloadFromWithProgress(host FileHost, sourceHash, dest string, progress io.Writer) error {
	return getlib.DownloadFile(HTTPClientFor(host), host.URL, sourceHash, dest, progress)
}

// DownloadSize returns the total number of bytes a download of sourceHash would
// transfer, recursing into directories. Callers use it to size a progress bar
// before starting the transfer.
func DownloadSize(host FileHost, sourceHash string) (int64, error) {
	return getlib.TotalSize(HTTPClientFor(host), host.URL, sourceHash)
}
