package webservice

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAttempts bounds how many times a request is retried on a transport-level
// error. Go does not auto-retry a POST when it reuses a keep-alive connection
// the server has since closed, so a long run of sequential writes (e.g. a big
// import) surfaces a bare EOF that would otherwise abort the whole operation.
// The request body is buffered in memory, so replaying it is free and safe: the
// write ops are idempotent (duplicate add is a no-op, set replaces, delete is
// idempotent).
const maxAttempts = 4

// retryBackoff returns the pause before attempt n (1-indexed): 100ms, 200ms,
// 400ms, ...
func retryBackoff(attempt int) time.Duration {
	return 100 * time.Millisecond * time.Duration(1<<(attempt-1))
}

type Client struct {
	server      string
	credentials Credentials
	hc          *http.Client
}

type Credentials struct {
	Username string
	Password string
}

func (c *Client) Run(request RequestInterface) (*Reply, error) {
	body, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		e := Reply{ReplyTypeEmpty, nil}
		return &e, errMarshal
	}

	// Retry only transport-level failures (Do error, response-body read error).
	// A Do that returns a *response* — including an auth rejection or a body we
	// then fail to unmarshal — means the server was reached, so we do not retry.
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(retryBackoff(attempt - 1))
		}

		req, err := http.NewRequest(http.MethodPost, c.server+"/"+request.GetId(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(c.credentials.Username, c.credentials.Password)

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, errors.New("Unauthorized")
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		replyStructPtr := request.GetReplyStructPtr()
		errUnmarshal := json.Unmarshal(respBody, replyStructPtr)
		if errUnmarshal != nil {
			return nil, errUnmarshal
		}

		reply := Reply{
			RequestName:    request.GetId(),
			ReplyStructPtr: replyStructPtr,
		}

		return &reply, nil
	}

	return nil, lastErr
}

// RunStream POSTs the request and hands the raw, unparsed response body to
// consume, which reads it incrementally (e.g. NDJSON). The body is never fully
// buffered in memory. Auth and non-200 failures are reported before consume is
// called; a failure mid-stream surfaces as a read error inside consume.
func (c *Client) RunStream(request RequestInterface, consume func(io.Reader) error) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}

	// Retry only the connection attempt (before any body byte is consumed); a
	// failure mid-stream still surfaces inside consume, since the reply is not
	// replayable once reading has started.
	var resp *http.Response
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(retryBackoff(attempt - 1))
		}

		req, reqErr := http.NewRequest(http.MethodPost, c.server+"/"+request.GetId(), bytes.NewReader(body))
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

	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("Unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("stream request failed: " + resp.Status)
	}

	return consume(resp.Body)
}

func NewClient(server, username, password string, insecure bool) *Client {
	c := &Client{}
	c.server = server
	c.credentials = Credentials{username, password}

	if len(c.server) < 5 {
		return nil
	}

	if strings.HasPrefix(c.server, "https") {
		t := tls.Config{InsecureSkipVerify: insecure}
		c.hc = &http.Client{Transport: &http.Transport{TLSClientConfig: &t}}
	} else {
		c.hc = &http.Client{}
	}

	return c
}
