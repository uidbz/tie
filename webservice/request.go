package webservice

import (
	"git.sr.ht/~uid/tie/tiedb"
)

const (
	ReplyTypeEmpty = "Empty"
)

type RequestInterface interface {
	GetId() string
	GetReplyStructPtr() interface{}
	Reply(username string, account func(string) tiedb.Collection) (Reply, error)
}

type Request struct {
	Id             string
	ReplyStructPtr interface{}
}

func (r *Request) GetId() string {
	return r.Id
}

func (r *Request) GetReplyStructPtr() interface{} {
	return r.ReplyStructPtr
}

type Reply struct {
	RequestName    string
	ReplyStructPtr interface{}
}

type ReplyStatus struct {
	Success bool
	Message string
}
