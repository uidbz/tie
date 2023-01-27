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
	Reply(environment *Environment) (Reply, error)
}

type Environment struct {
	Username   string
	Account    func(account string) *tiedb.Collection
	Collection func(namespace, collection string) *tiedb.Collection
	Webservice *Webservice
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

func (r ReplyStatus) GetSuccess() bool {
	return r.Success
}

func (r ReplyStatus) GetMessage() string {
	return r.Message
}

type ReplyInterface interface {
	GetSuccess() bool
	GetMessage() string
}

func ReadReply[T ReplyInterface](reply *Reply) *T {
	return reply.ReplyStructPtr.(*T)
}
