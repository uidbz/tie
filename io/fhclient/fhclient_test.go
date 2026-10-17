package fhclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// stub answers each route with a scripted status, and records what it saw.
type stub struct {
	status int
	body   string
	// ignoreRange makes it answer a ranged GET with 200 and the whole body,
	// like a server (or proxy) that does not support Range.
	ignoreRange bool

	gotReq   *http.Request
	gotBody  string
	gotRange string
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.gotReq = r
	s.gotRange = r.Header.Get("Range")
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		s.gotBody = string(b)
	}
	if s.status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	if s.status != 0 && s.status != http.StatusOK {
		http.Error(w, http.StatusText(s.status), s.status)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", "42")
		w.WriteHeader(http.StatusOK)
		return
	}
	if s.gotRange != "" && !s.ignoreRange {
		// Serve the requested slice, the way http.ServeContent does.
		var start, end int
		if _, err := fmt.Sscanf(s.gotRange, "bytes=0-%d", &end); err != nil {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start = 0
		if end+1 < len(s.body) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.body)))
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, s.body[start:end+1])
			return
		}
	}
	io.WriteString(w, s.body)
}

func withStub(t *testing.T, s *stub) *Client {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return New(srv.URL, srv.Client())
}

func TestStatStatuses(t *testing.T) {
	// 200: present, with the reported size.
	c := withStub(t, &stub{})
	ok, size, err := c.Stat(testHash)
	if err != nil || !ok || size != 42 {
		t.Errorf("Stat = (%v, %d, %v), want (true, 42, nil)", ok, size, err)
	}

	// 404: absent, not an error.
	c = withStub(t, &stub{status: http.StatusNotFound})
	ok, size, err = c.Stat(testHash)
	if err != nil || ok || size != 0 {
		t.Errorf("Stat 404 = (%v, %d, %v), want (false, 0, nil)", ok, size, err)
	}

	// 403: an error. Reporting it as "absent" would make a caller re-upload or
	// treat a referenced blob as missing.
	c = withStub(t, &stub{status: http.StatusForbidden})
	if ok, _, err := c.Stat(testHash); err == nil || ok {
		t.Errorf("Stat 403 = (%v, %v), want an error", ok, err)
	}
}

func TestGetStatuses(t *testing.T) {
	c := withStub(t, &stub{body: "payload"})
	body, err := c.Get(testHash)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "payload" {
		t.Errorf("body = %q", b)
	}

	c = withStub(t, &stub{status: http.StatusNotFound})
	if _, err := c.Get(testHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 404 = %v, want ErrNotFound", err)
	}

	c = withStub(t, &stub{status: http.StatusInternalServerError})
	if _, err := c.Get(testHash); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get 500 = %v, want a plain error", err)
	}
}

func TestGetRangeHonorsPartial(t *testing.T) {
	s := &stub{body: "0123456789"}
	c := withStub(t, s)
	data, partial, err := c.GetRange(testHash, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !partial || string(data) != "0123" {
		t.Errorf("GetRange = (%q, %v), want (\"0123\", true)", data, partial)
	}
	if s.gotRange != "bytes=0-3" {
		t.Errorf("Range header = %q, want bytes=0-3", s.gotRange)
	}

	// A server that ignores Range answers 200 with the whole body: the caller
	// must know the prefix is really everything.
	s = &stub{body: "0123456789", ignoreRange: true}
	c = withStub(t, s)
	data, partial, err = c.GetRange(testHash, 4)
	if err != nil {
		t.Fatal(err)
	}
	if partial || string(data) != "0123456789" {
		t.Errorf("GetRange (no Range support) = (%q, %v), want the whole body", data, partial)
	}
}

func TestPutSendsHeadersAndReportsTheAnswer(t *testing.T) {
	s := &stub{body: testHash}
	c := withStub(t, s)
	got, err := c.Put(testHash, strings.NewReader("body"), 4, PutOptions{
		Retention:  "72h",
		OwnerToken: "secret",
		Store:      "slow",
		MediaType:  "audio/flac",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != testHash {
		t.Errorf("confirmed hash = %q", got)
	}
	if s.gotBody != "body" {
		t.Errorf("body = %q", s.gotBody)
	}
	for h, want := range map[string]string{
		"Tie-Retention": "72h",
		"Tie-Owner":     "secret",
		"Tie-Store":     "slow",
		"Content-Type":  "audio/flac",
	} {
		if g := s.gotReq.Header.Get(h); g != want {
			t.Errorf("header %s = %q, want %q", h, g, want)
		}
	}
	if !strings.HasSuffix(s.gotReq.URL.Path, "/upload/"+testHash) {
		t.Errorf("path = %s", s.gotReq.URL.Path)
	}
}

// TestPutNon2xxIsNotAHash: the body of a failed upload is an error message, and
// treating it as a hash turns an auth failure into a checksum complaint.
func TestPutNon2xxIsNotAHash(t *testing.T) {
	c := withStub(t, &stub{status: http.StatusForbidden, body: "Forbidden"})
	got, err := c.Put(testHash, strings.NewReader("x"), 1, PutOptions{})
	if err == nil {
		t.Fatalf("a 403 upload returned %q", got)
	}
	if !strings.Contains(err.Error(), "Forbidden") || !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want the status and body", err)
	}
}

// TestPutProgressIsBestEffort: a progress bar whose max was sized from file
// bytes returns an error once the count passes it; that must not abort the
// upload.
func TestPutProgressIsBestEffort(t *testing.T) {
	c := withStub(t, &stub{body: testHash})
	if _, err := c.Put(testHash, strings.NewReader("payload"), 7, PutOptions{
		Progress: errWriter{},
	}); err != nil {
		t.Fatalf("a failing progress writer aborted the upload: %v", err)
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("current number exceeds max") }

// TestRejectsNonHashAddress keeps a crafted "hash" out of the URL: the path
// must never be built from something that is not a content address.
func TestRejectsNonHashAddress(t *testing.T) {
	s := &stub{}
	c := withStub(t, s)
	for _, bad := range []string{"", "../secret", strings.Repeat("z", 64), testHash[:63] + "g"} {
		if _, _, err := c.Stat(bad); err == nil {
			t.Errorf("Stat(%q) was accepted", bad)
		}
		if _, err := c.Get(bad); err == nil {
			t.Errorf("Get(%q) was accepted", bad)
		}
		if _, err := c.Put(bad, strings.NewReader("x"), 1, PutOptions{}); err == nil {
			t.Errorf("Put(%q) was accepted", bad)
		}
	}
	if s.gotReq != nil {
		t.Error("a request reached the server for a non-hash address")
	}
}

func TestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/version" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"server":"tie-filehost","build":{"version":"v0.6.1","commit":"abc"}}`)
	}))
	defer srv.Close()

	info, err := New(srv.URL, srv.Client()).Version()
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "v0.6.1" || info.Commit != "abc" {
		t.Errorf("version = %+v", info)
	}
	// A filehost without the endpoint must say so rather than fail to decode.
	if _, err := New(srv.URL+"/nope", srv.Client()).Version(); err == nil {
		t.Error("a missing /-/version was accepted")
	}
}

// sanity: the package's hash validation is the same one the rest of tie uses.
func TestHashValidationMatchesMetadata(t *testing.T) {
	if !metadata.IsHexHash(testHash) {
		t.Fatal("test constant is not a content address")
	}
}
