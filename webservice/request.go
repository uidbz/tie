package webservice

import (
	"io"

	"git.sr.ht/~uid/tie/tiedb"
)

const (
	ReplyTypeEmpty = "Empty"
)

type RequestInterface interface {
	GetId() string
	GetReplyStructPtr() interface{}
	Reply(environment *Environment) (Reply, error)
	// New returns a fresh, zero-valued instance of the same concrete request
	// type. RequestHandler unmarshals each incoming request into its own New()
	// instance so concurrent requests never share mutable state.
	New() RequestInterface
}

// StreamingRequestInterface is an optional interface a request may implement to
// write its reply directly to the response writer (e.g. NDJSON, one record per
// line) instead of returning a fully-buffered Reply. AnswerRequest prefers this
// path when present, so neither the daemon nor the client materializes the whole
// reply in memory. Once the first byte is written the HTTP status is committed,
// so a mid-stream error cannot change it (it can only be logged).
type StreamingRequestInterface interface {
	RequestInterface
	StreamReply(environment *Environment, w io.Writer) error
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
	OrigKey string
}

func ReadReply[T any](reply *Reply) *T {
	return reply.ReplyStructPtr.(*T)
}
