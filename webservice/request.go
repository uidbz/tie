package webservice

import (
	"github.com/uidbz/tie/tiedb"
)

// RequestID is all the transport needs from a request: it names itself, and it
// marshals to JSON. The reply type is a type parameter at the call site, so a
// request no longer carries a prototype of its own reply — the old
// GetReplyStructPtr() meant a request whose prototype disagreed with its
// handler's reply decoded into the wrong shape on the client, quietly.
type RequestID interface {
	GetId() string
}

// Environment is what a handler may reach: the authenticated user, and the
// collections.
type Environment struct {
	Username string
	// Collection resolves the request's namespace/collection. It returns an
	// error for a malformed name (every request) and ErrCollectionNotFound
	// for a read of an absent collection: reads never create collections.
	Collection func(namespace, collection string) (*tiedb.Collection, error)
	Webservice *Webservice
}

// Request is the part of a request every type shares: the id it is registered
// under, which is also the URL it is POSTed to.
type Request struct {
	Id string
}

func (r *Request) GetId() string {
	return r.Id
}

// ReplyStatus is the field block every reply carries: whether the operation
// succeeded, a message when it did not, and the key it was about.
type ReplyStatus struct {
	Success bool
	Message string
	OrigKey string
}
