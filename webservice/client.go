package webservice

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-resty/resty/v2"
)

type Client struct {
	server      string
	credentials Credentials
	r           *resty.Client
	// hc serves the streaming path (RunStream). Plain net/http gives clean,
	// incremental access to an unparsed response body, which resty's buffered
	// Run does not.
	hc *http.Client
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
	resp, err := c.r.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		SetBasicAuth(c.credentials.Username, c.credentials.Password).
		Post(c.server + "/" + request.GetId())

	defer func() {
		if body := resp.RawBody(); body != nil {
			body.Close()
		}
	}()

	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return nil, errors.New("Unauthorized")
	}

	replyStructPtr := request.GetReplyStructPtr()
	errUnmarshal := json.Unmarshal(resp.Body(), replyStructPtr)
	if errUnmarshal != nil {
		return nil, errUnmarshal
	}

	reply := Reply{
		RequestName:    request.GetId(),
		ReplyStructPtr: replyStructPtr,
	}

	return &reply, err
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
	req, err := http.NewRequest(http.MethodPost, c.server+"/"+request.GetId(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.credentials.Username, c.credentials.Password)

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
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
		c.r = resty.New().SetTLSClientConfig(&t)
		c.hc = &http.Client{Transport: &http.Transport{TLSClientConfig: &t}}
		if insecure {
			c.r.SetDisableWarn(true)
		}
	} else {
		c.r = resty.New()
		c.hc = &http.Client{}
		if strings.HasPrefix(c.server, "http://localhost") {
			c.r.SetDisableWarn(true)
		}
	}

	return c
}
