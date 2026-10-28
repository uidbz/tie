package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestSearchEmptyCollection: a search against a collection with no data is a
// successful empty result, not an error. This is the wire contract the GUI
// search boxes depend on ("nothing typed yet" / "no matches" are normal).
func TestSearchEmptyCollection(t *testing.T) {
	srv := registerAllServer(t)
	body := `{"Id":"Search","Namespace":"Collections","CollectionId":"Main","query":"abbey","relations":["album"]}`
	resp, err := http.Post(srv.URL+"/Search", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var reply SearchReply
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if got := string(envelope["matches"]); got != "[]" {
		t.Errorf("matches on the wire = %s, want []", got)
	}
	if !reply.Success {
		t.Errorf("Success = false, message %q", reply.Message)
	}
	if len(reply.Matches) != 0 || reply.TotalCount != 0 {
		t.Errorf("matches = %v, total = %d, want empty", reply.Matches, reply.TotalCount)
	}
}

// TestSearchUnknownIdIs404 guards the protocol surface: Search is registered,
// so it must never be answered as an unknown request.
func TestSearchUnknownIdIs404(t *testing.T) {
	srv := registerAllServer(t)
	resp, err := http.Post(srv.URL+"/NotASearch", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
