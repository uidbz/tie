package client

// Tests for TripleStoreVersion: the client-side twin of FileHostVersion, so a
// caller (tie-gui's connection panel, a monitoring probe) can ask either server
// what it runs over a plain GET, without an authenticated RPC round trip.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/tie/version"
)

func TestTripleStoreVersionReportsTheBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/version" {
			http.NotFound(w, r)
			return
		}
		if err := version.WriteJSON(w, "tie-triplestore"); err != nil {
			t.Errorf("WriteJSON: %v", err)
		}
	}))
	defer srv.Close()

	info, err := TripleStoreVersion(srv.URL)
	if err != nil {
		t.Fatalf("TripleStoreVersion: %v", err)
	}
	if info.GoVersion == "" {
		t.Errorf("GoVersion is empty in %+v: the body was not decoded", info)
	}

	// An older triplestore has no such route: the answer must say so rather than
	// fail to parse an HTML 404 page.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<html>404 page not found</html>", http.StatusNotFound)
	}))
	defer dead.Close()
	if _, err := TripleStoreVersion(dead.URL); err == nil || !strings.Contains(err.Error(), "/-/version") {
		t.Errorf("TripleStoreVersion against a server without the endpoint = %v, want an error naming /-/version", err)
	}
}

// TestVersionJSONIsOneShapeForBothServers: the filehost and the triplestore
// answer GET /-/version, and a caller that checks either must not need to know
// which one it is talking to.
func TestVersionJSONIsOneShapeForBothServers(t *testing.T) {
	for _, name := range []string{"tie-filehost", "tie-triplestore"} {
		var buf strings.Builder
		if err := version.WriteJSON(&buf, name); err != nil {
			t.Fatalf("WriteJSON(%s): %v", name, err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(buf.String()), &body); err != nil {
			t.Fatalf("decoding %s's version body: %v", name, err)
		}
		if body["server"] != name {
			t.Errorf("server = %v, want %s", body["server"], name)
		}
		build, ok := body["build"].(map[string]any)
		if !ok || build["goVersion"] == "" {
			t.Errorf("build = %v: want an object carrying goVersion", body["build"])
		}
	}
}
