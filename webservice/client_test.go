package webservice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testReply struct {
	Value string
}

type testRequest struct {
	Request
}

func (r *testRequest) Reply(*Environment) (Reply, error) { return Reply{}, nil }
func (r *testRequest) New() RequestInterface             { return &testRequest{} }

// readRequest carries an Id the server classifies as a read, so it is safe to
// replay after a transport failure.
type readRequest struct {
	Request
}

func (r *readRequest) Reply(*Environment) (Reply, error) { return Reply{}, nil }
func (r *readRequest) New() RequestInterface             { return &readRequest{} }

func newReadRequest() *readRequest {
	r := &readRequest{}
	r.Id = "Query"
	r.ReplyStructPtr = &testReply{}
	return r
}

// caseRequest selects a server behavior through a header, so one server can
// exercise several status codes.
type caseRequest struct {
	Request
	caseName string
}

func (r *caseRequest) Reply(*Environment) (Reply, error) { return Reply{}, nil }
func (r *caseRequest) New() RequestInterface             { return &caseRequest{caseName: r.caseName} }

func newCaseRequest(name string) *caseRequest {
	r := &caseRequest{caseName: name}
	r.Id = name
	r.ReplyStructPtr = &ReplyStatus{}
	return r
}

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

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	// A read: replaying it after a dropped connection is safe.
	reply, err := c.Run(newReadRequest())
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

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Run(newReadRequest()); err == nil {
		t.Fatal("Run returned nil error, want transport failure after exhausting retries")
	}
	if n := hits.Load(); n != maxAttempts {
		t.Fatalf("server saw %d requests, want %d", n, maxAttempts)
	}
}

// dropOnce drops the first request and answers the rest, so retry behavior is
// observable.
func dropOnceServer(hits *atomic.Int32, w http.ResponseWriter, r *http.Request) {
	if hits.Add(1) == 1 {
		dropConn(w)
		return
	}
	fmt.Fprint(w, `{"Value":"ok"}`)
}

// TestWriteIsNotRetried is the §4.11 guard: replaying a write whose response was
// lost reports an error for a write that already applied (an Update re-sent
// fails with "Did not find ..."). Only reads and Sync may be replayed.
func TestWriteIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dropOnceServer(&hits, w, r)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// newTestRequest has Id "test", which is not a read: not retry-safe.
	if _, err := c.Run(newTestRequest()); err == nil {
		t.Error("a dropped write reported success")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("write-classified request reached the server %d times, want 1 (no replay)", n)
	}
}

// TestReadIsRetried keeps the original reason for the retry: Go does not replay
// a POST on a closed keep-alive connection, so a long run of requests needs one
// retry to survive.
func TestReadIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dropOnceServer(&hits, w, r)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Run(newReadRequest())
	if err != nil {
		t.Fatalf("read should survive one dropped connection: %v", err)
	}
	if got := reply.ReplyStructPtr.(*testReply).Value; got != "ok" {
		t.Errorf("reply = %q", got)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("server saw %d requests, want 2 (drop + replay)", n)
	}
}

// TestRequestTimesOut: with no timeout at all, a stalled server hung the CLI,
// the FUSE mount and tie-smb forever.
func TestRequestTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		fmt.Fprint(w, `{"Value":"late"}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c.SetTimeout(200 * time.Millisecond)

	start := time.Now()
	if _, err := c.Run(newReadRequest()); err == nil {
		t.Error("a stalled server must not return success")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Run took %s; the per-request timeout did not apply", elapsed)
	}
}

// TestContextCancellationStopsRequest lets a caller cancel an in-flight request.
func TestContextCancellationStopsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, `{"Value":"late"}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c.SetTimeout(-1) // no client-side bound: the context is the only deadline

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := c.RunContext(ctx, newReadRequest()); err == nil {
		t.Error("a cancelled request must report an error")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("Run took %s after cancellation", elapsed)
	}
}

// TestNewClientRejectsBadURL: returning nil used to store a nil client and
// panic on the first request.
func TestNewClientRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "xyz", "localhost:1161"} {
		c, err := NewClient(bad, "", "", false)
		if err == nil {
			t.Errorf("NewClient(%q) accepted a non-URL", bad)
		}
		if c != nil {
			t.Errorf("NewClient(%q) returned a client alongside the error", bad)
		}
	}
	if _, err := NewClient("http://localhost:1161", "", "", false); err != nil {
		t.Errorf("a well-formed URL was rejected: %v", err)
	}
}

// TestStatusErrorsAreTyped: a 403's plaintext "Forbidden" used to reach
// json.Unmarshal and surface as "invalid character 'F'...".
func TestStatusErrorsAreTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "403":
			http.Error(w, "Forbidden", http.StatusForbidden)
		case "401":
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		case "404-json":
			w.WriteHeader(http.StatusNotFound)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Success":false,"Message":"collection not found"}`)
		case "200-fail":
			// An application answer that is a normal reply with Success:false
			// stays a reply: the client layer turns it into an error via
			// replyError, and Run must not pre-empt that with a StatusError.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Success":false,"Message":"Key has no associated values"}`)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		unwrap error
		want   string
	}{
		{"403", ErrForbidden, "Forbidden"},
		{"401", ErrUnauthorized, "Unauthorized"},
		{"404-json", nil, "collection not found"},
	} {
		req := newCaseRequest(tc.name)
		_, err := c.Run(req)
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if tc.unwrap != nil && !errors.Is(err, tc.unwrap) {
			t.Errorf("%s: errors.Is(%v, %v) = false", tc.name, err, tc.unwrap)
		}
		var se *StatusError
		if !errors.As(err, &se) {
			t.Errorf("%s: not a *StatusError: %v", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: message %q does not mention %q", tc.name, err.Error(), tc.want)
		}
		if strings.Contains(err.Error(), "invalid character") {
			t.Errorf("%s: the body still reached the JSON decoder: %q", tc.name, err.Error())
		}
	}

	// A 200 whose reply says Success:false is not an HTTP error: the reply has
	// to reach the client layer, which maps its message (ErrNotFound and
	// friends).
	reply, err := c.Run(newCaseRequest("200-fail"))
	if err != nil {
		t.Fatalf("a Success:false reply must not become a transport error: %v", err)
	}
	st := reply.ReplyStructPtr.(*ReplyStatus)
	if st.Success || !strings.Contains(st.Message, "no associated values") {
		t.Errorf("reply = %+v, want the server's Success:false body", st)
	}
}
