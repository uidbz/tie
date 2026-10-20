package webservice

// Tests for the triplestore's GET /-/version endpoint. The RPC protocol already
// answers a Version request, but only to a client that speaks the envelope and
// authenticates — so an operator with curl could identify a live *filehost*
// (`GET /-/version`) and not a live *triplestore*, which is exactly the question
// when deciding whether the fix that just landed is the one this box runs.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uidbz/tie/auth"
)

func versionTestServer(t *testing.T, anon string) *httptest.Server {
	t.Helper()
	store, err := auth.BuildStore(nil, anon, auth.RoleNone)
	if err != nil {
		t.Fatalf("BuildStore(%q): %v", anon, err)
	}
	ws := NewWebservice(WebserviceConfig{Auth: store})
	ws.HandleVersion("tie-triplestore")
	srv := httptest.NewServer(ws.mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleVersionServesBuildInfo(t *testing.T) {
	srv := versionTestServer(t, "read")

	resp, err := http.Get(srv.URL + "/-/version")
	if err != nil {
		t.Fatalf("GET /-/version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /-/version = %s, want 200", resp.Status)
	}
	var body struct {
		Server string         `json:"server"`
		Build  map[string]any `json:"build"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the version body: %v", err)
	}
	if body.Server != "tie-triplestore" {
		t.Errorf("server = %q, want tie-triplestore", body.Server)
	}
	if v, _ := body.Build["version"].(string); v == "" {
		t.Errorf("build.version is empty in %v: a caller cannot identify the build", body.Build)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestHandleVersionGatedByAuth: the endpoint is a build string, not a secret,
// but it sits at the same access level as the Version RPC request so a store
// configured to require credentials does not answer it to a stranger.
func TestHandleVersionGatedByAuth(t *testing.T) {
	srv := versionTestServer(t, "") // empty anonymous access means "none"

	resp, err := http.Get(srv.URL + "/-/version")
	if err != nil {
		t.Fatalf("GET /-/version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		t.Errorf("unauthenticated GET /-/version = %s (%s), want 401", resp.Status, body)
	}
}

// TestVersionRouteIsNotTheRPCPath: the triplestore serves POST /{request} for
// the protocol; the GET must not shadow or be shadowed by it.
func TestVersionRouteIsNotTheRPCPath(t *testing.T) {
	srv := versionTestServer(t, "read")

	resp, err := http.Post(srv.URL+"/version", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("POST /version answered like the version endpoint; the GET route must not widen the RPC path")
	}
}
