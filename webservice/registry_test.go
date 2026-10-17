package webservice

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/tie/auth"
)

// echoRequest and echoReply exercise the registry's decode-and-encode path.
type echoRequest struct {
	Request
	Word string
}

type echoReply struct {
	ReplyStatus
	Word string
}

func echoHandler(env *Environment, req *echoRequest) (*echoReply, error) {
	return &echoReply{
		ReplyStatus: ReplyStatus{Success: true},
		Word:        req.Word,
	}, nil
}

// TestRegistryOwnsDecodeAndEncode: the registry unmarshals into a fresh request
// of the registered type and marshals what the handler returns — the part every
// request file used to carry itself (§7.5).
func TestRegistryOwnsDecodeAndEncode(t *testing.T) {
	srv := handlerServer(t, 0, func(ws *Webservice) {
		Register(ws, "echo", auth.AccessWrite, echoHandler)
	})

	resp := post(t, srv.URL+"/echo", `{"Id":"echo","Word":"hello"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var rep echoReply
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("reply is not the registered reply shape: %q: %v", body, err)
	}
	if !rep.Success || rep.Word != "hello" {
		t.Errorf("reply = %+v, want the decoded Word back", rep)
	}
}

// TestMalformedBodyIs400: decoding is the registry's job, so a request body that
// is not JSON for the registered type is refused before any handler runs.
func TestMalformedBodyIs400(t *testing.T) {
	srv := handlerServer(t, 0, func(ws *Webservice) {
		Register(ws, "echo", auth.AccessWrite, echoHandler)
	})

	resp := post(t, srv.URL+"/echo", "{not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body: status = %d, want 400", resp.StatusCode)
	}
}

// TestAccessComesFromRegistration: the read/write classification used to live in
// a map kept apart from the request list, so a request could be registered and
// simply missing from the table — and the omission was invisible. The access
// level is now part of registering, and the authorization decision reads it from
// there (§7.5).
func TestAccessComesFromRegistration(t *testing.T) {
	store := auth.NewStore(
		map[string]auth.User{"reader": {Password: "pw", Role: auth.RoleRead}},
		auth.RoleNone,
	)
	ws := NewWebservice(WebserviceConfig{DbPath: t.TempDir(), Auth: store})
	Register(ws, "peek", auth.AccessRead, echoHandler)
	Register(ws, "poke", auth.AccessWrite, echoHandler)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		id   string
		want int
	}{
		{"peek", http.StatusOK},        // registered as a read: a read-only user is served
		{"poke", http.StatusForbidden}, // registered as a write: the same user is refused
	} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/"+tc.id, strings.NewReader(`{"Id":"`+tc.id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("reader", "pw")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s with a read-only user: status = %d, want %d", tc.id, resp.StatusCode, tc.want)
		}
	}
}

// TestUnregisteredDefaultsToWrite: an id nobody registered must not be reachable
// with read-only credentials — the fail-safe default from §4.15 still holds now
// that the classification comes from the registry.
func TestUnregisteredDefaultsToWrite(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{DbPath: t.TempDir(), Auth: auth.NewStore(nil, auth.RoleWrite)})
	Register(ws, "peek", auth.AccessRead, echoHandler)

	if got := ws.AccessFor("peek"); got != auth.AccessRead {
		t.Errorf("AccessFor(peek) = %v, want read", got)
	}
	if got := ws.AccessFor("NeverRegistered"); got != auth.AccessWrite {
		t.Errorf("AccessFor(never registered) = %v, want write", got)
	}
}

// TestStreamHandlerWritesThrough: a streaming registration hands the handler the
// response writer, so a large reply never sits in memory whole.
func TestStreamHandlerWritesThrough(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{DbPath: t.TempDir(), Auth: auth.NewStore(nil, auth.RoleWrite)})
	RegisterStream(ws, "stream", auth.AccessRead, func(env *Environment, req *echoRequest, w io.Writer) error {
		for _, word := range []string{"one", "two"} {
			if _, err := w.Write([]byte(word + "\n")); err != nil {
				return err
			}
		}
		return nil
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := post(t, srv.URL+"/stream", `{"Id":"stream"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "one\ntwo\n" {
		t.Errorf("body = %q, want the streamed lines", body)
	}
}
