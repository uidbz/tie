// Package fhclient is the one HTTP client for a tie filehost: Stat (HEAD), Get,
// GetRange, Put and Version.
//
// Three places used to implement "HEAD a blob" and two implemented a ranged
// GET, each with its own idea of what a status code means (§8 S6): verify
// treated 403 as "missing", repair reported the short hash, the media rescan
// accepted 206 while getlib rejected anything but 200. This package is the
// single place that decides, so a status-handling fix lands once.
//
// io/putlib and io/getlib are built on it: they add the parts that are theirs
// alone (hash verification while streaming, progress, directory manifests).
package fhclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/version"
)

// Client talks to one filehost. It is safe for concurrent use, and the same
// Client should be reused: its http.Client pools connections.
type Client struct {
	// URL is the filehost root, without a trailing slash.
	URL string
	// HTTP is the transport. Nil selects http.DefaultClient.
	HTTP *http.Client
	// Store optionally names the blob store to write to (the Tie-Store header).
	Store string
}

// New builds a client for one filehost root URL.
func New(url string, hc *http.Client) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), HTTP: hc}
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// blobURL validates the content address before it reaches the URL. The
// filehost validates too; this keeps a crafted value from turning into a
// request for some other path.
func (c *Client) blobURL(hash string) (string, error) {
	if !metadata.IsHexHash(hash) {
		return "", fmt.Errorf("fhclient: %q is not a content address", hash)
	}
	return c.URL + "/" + hash, nil
}

// Stat reports whether the filehost holds hash, with its size.
//
// A 404 means absent and is not an error. Every other non-200 is an error: a
// 403 or a 5xx must never be reported as "the blob is gone", because callers
// use this to decide what to re-upload or reaping-wise what to keep.
func (c *Client) Stat(hash string) (exists bool, size int64, err error) {
	return c.StatContext(context.Background(), hash)
}

// StatContext is Stat carrying the caller's context: cancelling ctx abandons the
// request rather than waiting for the filehost to answer (#114).
func (c *Client) StatContext(ctx context.Context, hash string) (exists bool, size int64, err error) {
	url, err := c.blobURL(hash)
	if err != nil {
		return false, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, 0, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return false, 0, err
	}
	if resp.Body != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, resp.ContentLength, nil
	case http.StatusNotFound:
		return false, 0, nil
	default:
		return false, 0, fmt.Errorf("filehost stat %s: %s", hash, resp.Status)
	}
}

// Get returns the blob's body; the caller must close it. A 404 is reported as
// ErrNotFound so a caller can tell "absent" from "server said no".
func (c *Client) Get(hash string) (io.ReadCloser, error) {
	body, _, err := c.GetContent(hash)
	return body, err
}

// GetContext is Get carrying the caller's context: cancelling ctx abandons the
// transfer, including one already in flight (#114).
func (c *Client) GetContext(ctx context.Context, hash string) (io.ReadCloser, error) {
	body, _, err := c.GetContentContext(ctx, hash)
	return body, err
}

// GetContent returns the blob's body plus the size the server reported
// (-1 when it did not say). Callers that size a progress bar or sniff a
// manifest prefix from the length use it instead of buffering.
func (c *Client) GetContent(hash string) (body io.ReadCloser, size int64, err error) {
	return c.GetContentContext(context.Background(), hash)
}

// GetContentContext is GetContent carrying the caller's context (#114).
func (c *Client) GetContentContext(ctx context.Context, hash string) (body io.ReadCloser, size int64, err error) {
	resp, err := c.get(ctx, hash, nil)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// ErrNotFound says the filehost has no such blob (a 404 answer).
var ErrNotFound = fmt.Errorf("blob not found")

// GetRange fetches the first n bytes of a blob. partial reports whether the
// server honored the Range request (206); when it answered 200 instead, data
// holds the whole blob and the caller should treat the prefix as a full read.
func (c *Client) GetRange(hash string, n int) (data []byte, partial bool, err error) {
	return c.GetRangeContext(context.Background(), hash, n)
}

// GetRangeContext is GetRange carrying the caller's context (#114).
func (c *Client) GetRangeContext(ctx context.Context, hash string, n int) (data []byte, partial bool, err error) {
	url, err := c.blobURL(hash)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		b, err := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
		return b, true, err
	case http.StatusOK:
		b, err := io.ReadAll(resp.Body)
		return b, false, err
	case http.StatusNotFound:
		return nil, false, ErrNotFound
	default:
		return nil, false, fmt.Errorf("filehost GET %s: %s", hash, resp.Status)
	}
}

// get is the shared GET path for non-ranged requests.
func (c *Client) get(ctx context.Context, hash string, extra map[string]string) (*http.Response, error) {
	url, err := c.blobURL(hash)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNotFound
	default:
		body := readSnip(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("filehost GET %s: %s%s", hash, resp.Status, body)
	}
}

// PutOptions carries the optional upload headers and progress hook.
type PutOptions struct {
	// Retention is the Tie-Retention header (a Go duration or "infinite").
	Retention string
	// OwnerToken is the Tie-Owner header, the secret that later lets the owner
	// change the blob's retention.
	OwnerToken string
	// Store names the target store; empty falls back to Client.Store.
	Store string
	// MediaType, when set, is sent as Content-Type.
	MediaType string
	// Progress, when non-nil, receives every chunk of the body that is sent.
	// Its errors are discarded: progress is cosmetic and must never tear down
	// an in-flight upload.
	Progress io.Writer
}

// Headers returns the upload headers for opts, in the order the filehost
// expects.
func (c *Client) Headers(opts PutOptions) http.Header {
	h := http.Header{}
	if opts.MediaType != "" {
		h.Set("Content-Type", opts.MediaType)
	}
	if opts.Retention != "" {
		h.Set("Tie-Retention", opts.Retention)
	}
	if opts.OwnerToken != "" {
		h.Set("Tie-Owner", opts.OwnerToken)
	}
	store := opts.Store
	if store == "" {
		store = c.Store
	}
	if store != "" {
		h.Set("Tie-Store", store)
	}
	return h
}

// Put uploads body under the content address hash and returns the hash the
// server answered. A non-2xx body is an error message (401/403 from auth, 400
// for a bad store or retention, 5xx), never a hash, so it is surfaced as-is
// instead of becoming a misleading checksum mismatch.
func (c *Client) Put(hash string, body io.Reader, size int64, opts PutOptions) (string, error) {
	return c.PutContext(context.Background(), hash, body, size, opts)
}

// PutContext is Put carrying the caller's context: cancelling ctx abandons the
// upload, including one whose body is already streaming (#114).
func (c *Client) PutContext(ctx context.Context, hash string, body io.Reader, size int64, opts PutOptions) (string, error) {
	if !metadata.IsHexHash(hash) {
		return "", fmt.Errorf("fhclient: %q is not a content address", hash)
	}
	if opts.Progress != nil {
		body = io.TeeReader(body, progressSink{opts.Progress})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.URL+"/upload/"+hash, body)
	if err != nil {
		return "", err
	}
	for k, vs := range c.Headers(opts) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("filehost upload %s: %s: %s", hash, resp.Status, strings.TrimSpace(string(answer)))
	}
	return strings.TrimSpace(string(answer)), nil
}

// progressSink discards the progress writer's errors. Upload progress is
// cosmetic, and the total written can legitimately exceed a bar sized from file
// bytes alone (a directory upload also streams one manifest per directory),
// which makes the bar return "current number exceeds max". Propagating that
// into the request body read would tear down the in-flight stream.
type progressSink struct{ w io.Writer }

func (s progressSink) Write(p []byte) (int, error) {
	_, _ = s.w.Write(p)
	return len(p), nil
}

// Version asks the filehost which build it is running (GET /-/version).
func (c *Client) Version() (version.Info, error) {
	resp, err := c.client().Get(c.URL + "/-/version")
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

// readSnip returns a short, printable excerpt of a response body for an error
// message, so a proxy's HTML error page does not flood the log.
func readSnip(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(r, 200))
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	return ": " + s
}
