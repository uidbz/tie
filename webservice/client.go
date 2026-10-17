package webservice

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAttempts bounds how many times a request is retried on a transport-level
// error. Go does not auto-retry a POST when it reuses a keep-alive connection
// the server has since closed, so a long run of sequential writes (e.g. a big
// import) surfaces a bare EOF that would otherwise abort the whole operation.
const maxAttempts = 4

// retryBackoff returns the pause before attempt n (1-indexed): 100ms, 200ms,
// 400ms, ...
func retryBackoff(attempt int) time.Duration {
	return 100 * time.Millisecond * time.Duration(1<<(attempt-1))
}

// retrySafe reports whether a request may be re-sent after a transport failure.
// Reads change nothing and Sync is idempotent by nature. Writes are not: an
// Update whose response was lost may already have applied, and re-sending it
// then fails with "Did not find ..." — the client would report an error for a
// write that succeeded (§4.11). A Batch containing an update has the same
// problem, so it is not retried either.
// readOnlyRequests are the request ids that change nothing server-side. The
// server records access when a request is registered, but retrying is a client
// decision, so the client keeps its own list — deliberately narrower in spirit:
// Sync is idempotent too, and a Batch that contains an update is not.
var readOnlyRequests = map[string]bool{
	"Dummy":      true,
	"Query":      true,
	"Expand":     true,
	"Associated": true,
	"CoTags":     true,
	"Dump":       true,
	"Version":    true,
}

func retrySafe(reqID string) bool {
	return readOnlyRequests[reqID] || reqID == "Sync"
}

// defaultRequestTimeout bounds one non-streaming request. Without any timeout a
// stalled server hung the CLI, the FUSE mount and tie-smb forever (§4.11). It
// deliberately does not apply to RunStream, whose body (a Dump of a large
// collection) can legitimately run for minutes.
const defaultRequestTimeout = 60 * time.Second

type Client struct {
	server      string
	credentials Credentials
	hc          *http.Client
	// timeout bounds a single Run. Zero selects defaultRequestTimeout; a
	// negative value disables it.
	timeout time.Duration
}

type Credentials struct {
	Username string
	Password string
}

// ErrUnauthorized and ErrForbidden let callers branch on the two auth failures
// without matching on the server's text.
var (
	ErrUnauthorized = errors.New("unauthorized: check Username/Password in your config")
	ErrForbidden    = errors.New("forbidden: your role is not sufficient for this request")
)

// StatusError is an HTTP-level failure from the triplestore. The server keeps a
// JSON ReplyStatus body on error answers, so Message carries what the server
// said; when the body was not a reply at all (a proxy's HTML error page) Body
// carries a snippet instead. Before this, a 403's plaintext "Forbidden" reached
// json.Unmarshal and surfaced as "invalid character 'F'..." (§4.11).
type StatusError struct {
	Status  int
	Request string
	Message string
	Body    string
}

func (e *StatusError) Error() string {
	detail := e.Message
	if detail == "" {
		detail = e.Body
	}
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s: %s: %s", e.Request, http.StatusText(e.Status), detail)
}

// Unwrap maps the two auth statuses onto sentinels so errors.Is works.
func (e *StatusError) Unwrap() error {
	switch e.Status {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	}
	return nil
}

// SetTimeout bounds one non-streaming request. Zero restores the default, a
// negative value disables the bound.
func (c *Client) SetTimeout(d time.Duration) { c.timeout = d }

// effectiveTimeout resolves the timeout field.
func (c *Client) effectiveTimeout() time.Duration {
	switch {
	case c.timeout < 0:
		return 0
	case c.timeout == 0:
		return defaultRequestTimeout
	default:
		return c.timeout
	}
}

// Call sends req and decodes the reply as Rep. The reply type is a parameter,
// so nothing has to keep a prototype of the reply in sync with the handler:
// disagreeing types are a compile error at the call site, not a wrong-shaped
// decode at runtime.
func Call[Req RequestID, Rep any](c *Client, req Req) (*Rep, error) {
	return CallContext[Req, Rep](context.Background(), c, req)
}

// CallContext sends req honoring ctx and the client's per-request timeout
// (whichever is shorter). Only requests classified by retrySafe are replayed
// after a transport failure.
func CallContext[Req RequestID, Rep any](ctx context.Context, c *Client, req Req) (*Rep, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, req.GetId(), body)
	if err != nil {
		return nil, err
	}
	rep := new(Rep)
	if err := json.Unmarshal(resp.body, rep); err != nil {
		return nil, fmt.Errorf("webservice returned %s: %s", resp.status, snippet(resp.body))
	}
	return rep, nil
}

// response is one answered request: the status line and the body the server
// wrote.
type response struct {
	code   int
	status string
	body   []byte
}

// send POSTs body to /<id> and returns the answer. Retry only transport-level
// failures (Do error, response-body read error): a Do that returns a *response*
// means the server was reached, so the answer — including an auth rejection —
// is final.
func (c *Client) send(ctx context.Context, id string, body []byte) (*response, error) {
	attempts := maxAttempts
	if !retrySafe(id) {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(retryBackoff(attempt - 1)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		reqCtx, cancel, err := c.requestContext(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.server+"/"+id, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(c.credentials.Username, c.credentials.Password)

		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, c.statusError(id, resp, respBody)
		}

		return &response{code: resp.StatusCode, status: resp.Status, body: respBody}, nil
	}

	return nil, lastErr
}

// requestContext applies the client's per-request timeout on top of the
// caller's context.
func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}
	to := c.effectiveTimeout()
	if to == 0 {
		return ctx, func() {}, nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, to)
	return reqCtx, cancel, nil
}

// statusError turns a non-2xx answer into a typed error, decoding the server's
// ReplyStatus body when it is one so the message survives.
func (c *Client) statusError(reqID string, resp *http.Response, body []byte) error {
	se := &StatusError{Status: resp.StatusCode, Request: reqID}
	var st ReplyStatus
	if err := json.Unmarshal(body, &st); err == nil && st.Message != "" {
		se.Message = st.Message
	} else {
		se.Body = snippet(body)
	}
	return se
}

// snippet is a short, printable excerpt of a response body for error messages.
func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// RunStream POSTs the request with a background context and hands the raw,
// unparsed response body to consume.
func (c *Client) RunStream(request RequestID, consume func(io.Reader) error) error {
	return c.RunStreamContext(context.Background(), request, consume)
}

// RunStreamContext POSTs the request and hands the raw, unparsed response body
// to consume, which reads it incrementally (e.g. NDJSON). The body is never
// fully buffered in memory. Auth and non-200 failures are reported before
// consume is called; a failure mid-stream surfaces as a read error inside
// consume. No per-request timeout is imposed: a large stream is legitimate, so
// callers that need a deadline pass a context with one.
func (c *Client) RunStreamContext(ctx context.Context, request RequestID, consume func(io.Reader) error) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}

	// Retry only the connection attempt (before any body byte is consumed); a
	// failure mid-stream still surfaces inside consume, since the reply is not
	// replayable once reading has started. A stream is a read, so it is always
	// safe to re-establish.
	var resp *http.Response
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(retryBackoff(attempt - 1)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/"+request.GetId(), bytes.NewReader(body))
		if reqErr != nil {
			return reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(c.credentials.Username, c.credentials.Password)

		resp, lastErr = c.hc.Do(req)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return lastErr
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return c.statusError(request.GetId(), resp, body)
	}

	return consume(resp.Body)
}

// NewClient builds a client for one triplestore. A URL that cannot be a
// triplestore address is an error: returning nil used to store a nil client and
// panic on the first request (§4.11).
func NewClient(server, username, password string, insecure bool) (*Client, error) {
	if len(server) < 5 || !(strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://")) {
		return nil, fmt.Errorf("webservice: %q is not a triplestore URL (want http://host:port)", server)
	}
	c := &Client{
		server:      strings.TrimSuffix(server, "/"),
		credentials: Credentials{username, password},
	}
	if strings.HasPrefix(server, "https") {
		t := tls.Config{InsecureSkipVerify: insecure}
		c.hc = &http.Client{Transport: &http.Transport{TLSClientConfig: &t}}
	} else {
		c.hc = &http.Client{}
	}
	return c, nil
}
