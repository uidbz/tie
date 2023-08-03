package webservice

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-resty/resty/v2"
)

type Client struct {
	server      string
	credentials Credentials
	r           *resty.Client
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
		resp.RawBody().Close()
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

func NewClient(server, username, password string) *Client {
	c := &Client{}
	c.server = server
	c.credentials = Credentials{username, password}

	if len(c.server) < 5 {
		return nil
	}

	if strings.HasPrefix(c.server, "https") {
		t := tls.Config{}
		t.InsecureSkipVerify = true // Not so good. Temp hack for self-signed certificates.
		c.r = resty.New().SetTLSClientConfig(&t)
	} else {
		c.r = resty.New()
		if strings.HasPrefix(c.server, "http://localhost") {
			c.r.SetDisableWarn(true)
		}
	}

	return c
}
