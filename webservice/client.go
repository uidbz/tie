package webservice

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-resty/resty/v2"
)

type Client struct {
	server      string
	credentials Credentials
}

type Credentials struct {
	Username string
	Password string
}

func (c *Client) Run(request RequestInterface) (*Reply, error) {
	if len(c.server) < 5 {
		e := Reply{ReplyTypeEmpty, nil}
		return &e, errors.New("Webservice not set")
	}
	var r *resty.Client
	if c.server[0:5] == "https" {
		t := tls.Config{}
		t.InsecureSkipVerify = true // Not so good. Temp hack for self-signed certificates.
		r = resty.New().SetTLSClientConfig(&t)
	} else {
		r = resty.New()
	}
	body, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		e := Reply{ReplyTypeEmpty, nil}
		return &e, errMarshal
	}
	resp, err := r.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		SetBasicAuth(c.credentials.Username, c.credentials.Password).
		Post(c.server + "/" + request.GetId())

	if err != nil {
		return nil, err
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return nil, errors.New("Unauthorized")
	}

	replyStructPtr := request.GetReplyStructPtr()
	errUnmarshal := json.Unmarshal(resp.Body(), &replyStructPtr)
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

	return c
}
