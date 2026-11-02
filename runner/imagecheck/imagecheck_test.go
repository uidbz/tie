package imagecheck

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func registryReturning(status int, seen *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Method + " " + r.URL.Path)
		w.WriteHeader(status)
	}))
}

func TestCheck(t *testing.T) {
	var seen atomic.Value

	notFound := registryReturning(http.StatusNotFound, &seen)
	defer notFound.Close()
	if err := Check(notFound.URL, "tool:v2"); err == nil {
		t.Fatal("a definitive 404 must report the image missing")
	}
	if got := seen.Load().(string); got != "HEAD /v2/tool/manifests/v2" {
		t.Errorf("registry saw %q", got)
	}

	ok := registryReturning(http.StatusOK, &seen)
	defer ok.Close()
	if err := Check(ok.URL, "tool"); err != nil {
		t.Errorf("present image: %v", err)
	}
	if got := seen.Load().(string); got != "HEAD /v2/tool/manifests/latest" {
		t.Errorf("untagged handler: registry saw %q", got)
	}

	for _, status := range []int{http.StatusInternalServerError, http.StatusAccepted} {
		srv := registryReturning(status, &seen)
		if err := Check(srv.URL, "tool:v2"); err != nil {
			t.Errorf("status %d must fail open, got %v", status, err)
		}
		srv.Close()
	}

	// Unreachable registry: fail open.
	dead := registryReturning(http.StatusOK, &seen)
	deadURL := dead.URL
	dead.Close()
	if err := Check(deadURL, "tool:v2"); err != nil {
		t.Errorf("unreachable registry must fail open, got %v", err)
	}

	if err := Check("", "tool:v2"); err != nil {
		t.Errorf("no registry configured must skip the check, got %v", err)
	}
	if err := Check(ok.URL, ""); err != nil {
		t.Errorf("empty handler: %v", err)
	}
}

func TestSchemeAndSlashNormalization(t *testing.T) {
	var seen atomic.Value
	srv := registryReturning(http.StatusOK, &seen)
	defer srv.Close()
	// A configured registry with a trailing slash must not produce a
	// double-slash manifest path.
	if err := Check(srv.URL+"/", "a/tool:tag"); err != nil {
		t.Fatalf("trailing slash in registry: %v", err)
	}
	if got := seen.Load().(string); got != "HEAD /v2/a/tool/manifests/tag" {
		t.Errorf("saw %q", got)
	}
	// Schemeless config values are attempted over https; against this
	// plain test server that is a transport error, which must fail open.
	if err := Check(strings.TrimPrefix(srv.URL, "http://"), "tool:v2"); err != nil {
		t.Errorf("schemeless registry must fail open, got %v", err)
	}
}
