// Package runclient is the client library for a tie runner: the frozen
// HTTP surface documented in docs/runner-api.md, plus the filehost
// upload/download helpers sized for job work (upload one input, upload a
// batch composed into a single tree, download a result by hash),
// progress watches, and fleet fan-out.
//
// Request and response records are the frozen runner/api types, the
// same ones the runner marshals, so client and server cannot drift.
// Where a client needs to remember which runner reported a value (fleet
// merge views), the tag lives in the Keyed wrapper, never in a wire
// field.
//
// Every runner call goes through do(): one client, one error order
// (transport error -> non-2xx -> unmarshal), typed sentinels for
// 401/404 so callers branch with errors.Is instead of comparing status
// codes everywhere, and url.PathEscape'd path segments.
package runclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnauthorized is what a 401 unwraps to: the DeployKey (or admin
	// token) was missing or wrong.
	ErrUnauthorized = errors.New("unauthorized: wrong or missing DeployKey")
	// ErrNotFound is what a 404 unwraps to.
	ErrNotFound = errors.New("not found")
)

// AuthUser is the Basic Auth user every keyed request carries. Runners
// authorize against a deployment-configured user; the tie default is
// "tie". A branded shell sets this once at startup to match its
// runner's configuration.
var AuthUser = "tie"

// CLIName names the command in the watch-cancel hints ("resume with
// <CLIName> status <id>"). Watch functions print advice, and advice
// that names a binary the user never typed is a trap; a branded shell
// sets this once at startup, like AuthUser.
var CLIName = "tie-run"

// HTTPError is a non-2xx reply. Body is truncated so a proxy's HTML
// error page cannot flood the terminal.
type HTTPError struct {
	Method, URL string
	Status      int
	Body        string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: runner returned %d", e.Method, e.URL, e.Status)
	}
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// Unwrap maps the two statuses callers branch on to sentinels: the
// ActiveAll fallback, FindPipeline, and DeleteImage use
// errors.Is(err, ErrNotFound).
func (e *HTTPError) Unwrap() error {
	switch e.Status {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// call describes one runner request.
type call struct {
	Method  string
	Runner  string // base URL, trailing slash tolerated
	Path    string // route path, leading slash
	Key     string // DeployKey; "" sends no auth
	Query   url.Values
	Body    any
	Timeout time.Duration // 0 = defaultTimeout
}

// defaultTimeout bounds one runner request when the caller does not say
// otherwise; long-running calls (deploy, submit) pass an explicit
// Timeout.
const defaultTimeout = 30 * time.Second

// listTimeout bounds one fleet list request per server: one dead runner
// must not make a jobs listing hang for the full default.
const listTimeout = 10 * time.Second

// shared is one process-wide client: a reused connection pool. Every
// call used to pay a fresh TLS handshake, noticeable when polling at
// 2 Hz.
var shared = http.DefaultClient

// do runs one runner request with the one correct error order: transport
// error first, then a non-2xx as an HTTPError carrying status and a
// truncated body, then unmarshal with a body excerpt so a parse failure
// names what came back. The runner URL and key are normalised here, so
// the trailing-slash class cannot come back on a new endpoint.
func do(c call, out any) error {
	runner, key := normalizeTarget(c.Runner, c.Key)
	u := runner + c.Path
	if len(c.Query) > 0 {
		u += "?" + c.Query.Encode()
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var body io.Reader
	if c.Body != nil {
		b, err := json.Marshal(c.Body)
		if err != nil {
			return fmt.Errorf("%s %s: encoding request: %w", c.Method, u, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, u, body)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Method, u, err)
	}
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.SetBasicAuth(AuthUser, key)
		debugDump(c.Method, u, AuthUser, key)
	}

	resp, err := shared.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Method, u, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: reading reply: %w", c.Method, u, err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return &HTTPError{Method: c.Method, URL: u, Status: resp.StatusCode, Body: truncateBody(respBody, 512)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("%s %s: parsing reply: %w (body: %s)", c.Method, u, err, truncateBody(respBody, 200))
	}
	return nil
}

// CallRunner is the exported entry to do() for runner calls that live
// outside this package — the deploy POST to /api/deploymanager, which
// needs an explicit long timeout instead of the default.
func CallRunner(method, runner, path string, body, out any, timeout time.Duration) error {
	return do(call{Method: method, Runner: runner, Path: path, Body: body, Timeout: timeout}, out)
}

// normalizeTarget trims a trailing slash from the runner URL and
// whitespace from the DeployKey so a stray "/" in a config file
// (Runner = '.../') or a pasted key with spaces cannot silently produce
// a wrong request.
func normalizeTarget(runner, deployKey string) (string, string) {
	return strings.TrimRight(runner, "/"), strings.TrimSpace(deployKey)
}

// truncateBody shortens a response body for error messages so a proxy's
// HTML error page cannot flood the terminal.
func truncateBody(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// debugDump prints the outgoing management request when TIE_DEBUG is
// set. It never prints the key itself — only its length — so the dump
// is safe to paste into an issue.
func debugDump(method, url, user, key string) {
	if os.Getenv("TIE_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[tie-debug] %s %s (auth user=%q, key length=%d)\n",
		method, url, user, len(key))
}

// seg escapes one path segment: a registry-qualified image name
// (registry/ns/img:tag) or an ID with a stray ? or # used to be
// concatenated raw and could silently hit a different route.
func seg(s string) string { return url.PathEscape(s) }

// listQuery renders the shared since/user/status filters of the list
// endpoints as query parameters.
func listQuery(since time.Duration, user, status string) url.Values {
	q := url.Values{}
	if since > 0 {
		q.Set("since", sinceParam(since))
	}
	if user != "" {
		q.Set("user", user)
	}
	if status != "" {
		q.Set("status", status)
	}
	return q
}

// sinceParam renders a since filter the way the runner's
// time.ParseDuration expects: whole seconds ("2592000s"), not Go's
// GoString ("720h0m0s"). One format for jobs and pipelines alike.
func sinceParam(d time.Duration) string {
	return strconv.FormatInt(int64(d.Seconds()), 10) + "s"
}
