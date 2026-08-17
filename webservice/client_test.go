package webservice

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type testReply struct {
	Value string
}

type testRequest struct {
	Request
}

func (r *testRequest) Reply(*Environment) (Reply, error) { return Reply{}, nil }
func (r *testRequest) New() RequestInterface             { return &testRequest{} }

func newTestRequest() *testRequest {
	r := &testRequest{}
	r.Id = "test"
	r.ReplyStructPtr = &testReply{}
	return r
}

// dropConn abruptly closes the underlying connection without writing a
// response, simulating a server that closed a pooled keep-alive connection —
// the client's Do then returns an EOF-class error.
func dropConn(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	conn.Close()
}

func TestRunRetriesTransientFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			dropConn(w) // first attempt: drop the connection
			return
		}
		fmt.Fprint(w, `{"Value":"ok"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "", false)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}

	reply, err := c.Run(newTestRequest())
	if err != nil {
		t.Fatalf("Run returned error despite retry: %v", err)
	}
	got := reply.ReplyStructPtr.(*testReply).Value
	if got != "ok" {
		t.Fatalf("reply value = %q, want %q", got, "ok")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("server saw %d requests, want 2 (one drop + one success)", n)
	}
}

func TestRunFailsAfterMaxAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		dropConn(w) // always drop
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "", false)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}

	if _, err := c.Run(newTestRequest()); err == nil {
		t.Fatal("Run returned nil error, want transport failure after exhausting retries")
	}
	if n := hits.Load(); n != maxAttempts {
		t.Fatalf("server saw %d requests, want %d", n, maxAttempts)
	}
}
