package fhclient

// Tests for #114: the filehost path had no bound once a connection was
// accepted, and no way for a caller to abandon a transfer it no longer wants.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hash64 is a well-formed content address; the server below never checks it.
const hash64 = "0000000000000000000000000000000000000000000000000000000000000000"

func TestContextVariantsAbandonTransfers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := c.StatContext(ctx, hash64); !errors.Is(err, context.Canceled) {
		t.Errorf("StatContext with a cancelled context = %v, want context.Canceled", err)
	}
	if _, _, err := c.GetContentContext(ctx, hash64); !errors.Is(err, context.Canceled) {
		t.Errorf("GetContentContext = %v, want context.Canceled", err)
	}
	if _, err := c.GetContext(ctx, hash64); !errors.Is(err, context.Canceled) {
		t.Errorf("GetContext = %v, want context.Canceled", err)
	}
	if _, _, err := c.GetRangeContext(ctx, hash64, 16); !errors.Is(err, context.Canceled) {
		t.Errorf("GetRangeContext = %v, want context.Canceled", err)
	}
	if _, err := c.PutContext(ctx, hash64, strings.NewReader("body"), 4, PutOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("PutContext = %v, want context.Canceled", err)
	}
}

// TestPutIsCancellableMidBody: an upload the caller has given up on stops, even
// while its body is still being written.
func TestPutIsCancellableMidBody(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		if n > 0 {
			close(started)
		}
		// Keep draining the body: that is how the server notices a client that
		// has gone away, which is what cancels the request context.
		for {
			if _, err := r.Body.Read(buf); err != nil {
				break
			}
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.PutContext(ctx, hash64, strings.NewReader(strings.Repeat("x", 1<<20)), 1<<20, PutOptions{})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("the upload finished before it started: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("an upload cancelled mid-body returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled upload kept running")
	}
}
