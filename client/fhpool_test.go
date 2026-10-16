package client

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/uidbz/tie/io/fhclient"
)

// TestFileHostClientsArePooled is the §5.1 L19 guard: HTTPClientFor built a new
// transport for every call, so verify, repair, stat and the media rescan each
// carried their own connections — nothing was pooled, and a large run piled up
// sockets in TIME_WAIT.
func TestFileHostClientsArePooled(t *testing.T) {
	var newConns atomic.Int32
	// ConnState has to be installed before the server starts: it is read when
	// each connection is accepted.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	host := FileHost{URL: srv.URL, Username: "ops", Password: "secret"}

	// Same host config, same client: the transport (and its connection pool) is
	// shared rather than rebuilt.
	c1 := FileHostClient(host)
	c2 := FileHostClient(host)
	if c1 != c2 {
		t.Error("FileHostClient returned a new client per call, so connections are never pooled")
	}
	if HTTPClientFor(host) != HTTPClientFor(host) {
		t.Error("HTTPClientFor returned a new client per call")
	}
	// A different credential set must not share a transport: the credentials are
	// baked into the round tripper.
	if HTTPClientFor(host) == HTTPClientFor(FileHost{URL: srv.URL, Username: "other", Password: "x"}) {
		t.Error("two different credential sets shared one transport")
	}

	// Several requests through the shared client must reuse one connection.
	for i := 0; i < 3; i++ {
		resp, err := HTTPClientFor(host).Get(srv.URL + "/x")
		if err != nil {
			t.Fatal(err)
		}
		// Drain before closing, or the transport discards the connection
		// instead of reusing it.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := newConns.Load(); n != 1 {
		t.Errorf("the server saw %d new connections for 3 requests, want 1 (keep-alive reuse)", n)
	}
}

// BenchmarkStatPooled compares the shared, pooled client against the old
// behavior of building a transport per call, against the same server, so the
// cost the pool removes is visible: a TCP handshake plus TLS setup per request.
func BenchmarkStatPooled(b *testing.B) {
	srv := statServer(b)
	host := FileHost{URL: srv.URL, Username: "ops", Password: "secret"}
	fh := FileHostClient(host)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := fh.Stat(testBlobHash); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStatFreshTransport(b *testing.B) {
	srv := statServer(b)
	host := FileHost{URL: srv.URL, Username: "ops", Password: "secret"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// What HTTPClientFor used to do on every call.
		fh := fhclient.New(host.URL, &http.Client{Transport: &http.Transport{}})
		if _, _, err := fh.Stat(testBlobHash); err != nil {
			b.Fatal(err)
		}
	}
}

func statServer(b *testing.B) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(http.StatusOK)
	}))
	b.Cleanup(srv.Close)
	return srv
}

const testBlobHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
