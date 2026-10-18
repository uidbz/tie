package client

// Tests for #114: the filehost path had no bound once the connection was
// accepted, and neither the shared transports nor the TieClient façade let a
// caller set a deadline or abandon work it no longer wants. The triplestore path
// already bounds requests (webservice's 60 s default) — but the façade hid it.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// hashOfT is a well-formed content address for the paths that only need to look
// like one.
const hashOfT = "0000000000000000000000000000000000000000000000000000000000000000"

// TestStalledFilehostIsBounded is the whole point of the transport deadline: a
// filehost that accepts the TCP connection and then never answers — a half-dead
// tie-filehost, a captive portal, an NFS-backed store on a sleeping server —
// used to park the caller forever. Dial and TLS handshake are already bounded by
// the inherited DefaultTransport; everything after the connection was not.
func TestStalledFilehostIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
	defer srv.Close()

	FileHostResponseHeaderTimeout = 150 * time.Millisecond
	defer func() { FileHostResponseHeaderTimeout = defaultFileHostResponseHeaderTimeout }()

	// A credential profile of its own: the transport is shared per profile, so
	// this must not borrow one another test already built.
	fh := FileHostClient(FileHost{URL: srv.URL, Username: "stalled-profile"})
	done := make(chan error, 1)
	go func() {
		_, _, err := fh.Stat(hashOfT)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Stat against a filehost that never answered returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stat against a filehost that never answered is still parked: nothing bounded the wait")
	}
}

// TestSetTimeoutBoundsTriplestoreCalls: the façade exposes neither the timeout
// nor a context, so a caller could neither shorten the 60 s default nor learn
// that it exists.
func TestSetTimeoutBoundsTriplestoreCalls(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Short enough that the deferred Close does not dominate the test; the
		// assertion below measures the call, not the handler.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()

	cfg := TestingConfig()
	cfg.TripleStoreURL = slow.URL
	tie := NewTieClientFor(cfg, "verifytesting")
	tie.SetTimeout(150 * time.Millisecond)

	start := time.Now()
	_, err := tie.Get("anything")
	if err == nil {
		t.Fatal("a request to a triplestore that never answered returned no error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Get against a stalled triplestore = %v, want a deadline error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("SetTimeout(150ms) was not honored: the call took %v", d)
	}
}

// TestContextVariantsAbandonWork: an interactive caller that switches
// collection or closes a dialog must be able to stop the work in flight, not
// wait out the request and discard the answer.
func TestContextVariantsAbandonWork(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	mkdir(t, dir+"/keep")
	writeFile(t, dir+"/keep/a.txt", "one")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	uid, err := tie.DirUIDFromPath(FileURIScheme + dir + "/keep")
	if err != nil || uid == "" {
		t.Fatalf("DirUIDFromPath(keep) = %q, %v", uid, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := tie.GetContext(ctx, "anything"); !errors.Is(err, context.Canceled) {
		t.Errorf("GetContext with a cancelled context = %v, want context.Canceled", err)
	}
	if _, _, err := tie.QueryContext(ctx, QuerySpec{Terms: []string{"directory"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("QueryContext = %v, want context.Canceled", err)
	}
	if _, err := tie.AddContext(ctx, "k", "v1", "v2"); !errors.Is(err, context.Canceled) {
		t.Errorf("AddContext = %v, want context.Canceled", err)
	}
	if err := tie.SetContext(ctx, "k", "v1", []string{"v2"}); !errors.Is(err, context.Canceled) {
		t.Errorf("SetContext = %v, want context.Canceled", err)
	}
	if _, err := tie.DeleteContext(ctx, "k", "v1", "v2"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteContext = %v, want context.Canceled", err)
	}
	if _, err := ReadTieDirContext(ctx, tie, uid); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadTieDirContext = %v, want context.Canceled", err)
	}

	hash := hashOf(t, dir+"/keep/a.txt")
	dest := filepath.Join(t.TempDir(), "a.txt")
	if err := tie.DownloadContext(ctx, "", hash, dest); !errors.Is(err, context.Canceled) {
		t.Errorf("DownloadContext = %v, want context.Canceled", err)
	}
	bc, err := tie.BlobClient("")
	if err != nil {
		t.Fatalf("BlobClient: %v", err)
	}
	if _, err := bc.GetContext(ctx, hash); !errors.Is(err, context.Canceled) {
		t.Errorf("the blob client's GetContext = %v, want context.Canceled", err)
	}
}
