package client

// context.go carries a caller's intent through the façade (#114).
//
// The webservice layer already supported both a deadline and a context, and the
// filehost layer had always taken an *http.Client — but neither reached the
// caller through TieClient, so an embedder could discover that a request was
// parked only after it had hung. Every Context variant added here keeps the
// behaviour of the method it wraps and gives the caller a way to stop waiting.
//
// Cancelling is not a rollback. A cancelled Add or Batch leaves whatever the
// server already applied; what makes an interrupted run safe is the journal the
// repair and import paths write, not the cancellation.

import (
	"time"
)

// SetTimeout bounds one triplestore request: it sets the deadline on the client
// this TieClient makes its calls through. Negative means no timeout; zero
// restores the library default.
//
// The default (60 s, see the webservice package) is what a long Dump or Import
// needs, which is why it is not shortened by default. A caller with a UI in
// front of it usually wants both knobs: a deadline for the request it is waiting
// on, and a context it can cancel when the user changes their mind.
//
// Note that scoped clients share one transport where they share a scope (see
// In): setting the timeout on one affects the others that reuse that client.
func (tc *TieClient) SetTimeout(d time.Duration) {
	if tc.transport != nil {
		tc.transport.SetTimeout(d)
	}
}
