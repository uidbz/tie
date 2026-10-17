package webservice

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/tie/auth"
)

// failingRequest answers with an application error, so the registry's error
// status codes can be observed.
type failingRequest struct {
	Request
}

func failingHandler(err error) Handler[failingRequest, ReplyStatus] {
	return func(env *Environment, req *failingRequest) (*ReplyStatus, error) {
		return nil, err
	}
}

// handlerServer serves a webservice with the given registrations. A
// registration is a function because Register is generic: the request and reply
// types are chosen at the call site, not passed as values.
func handlerServer(t *testing.T, maxBytes int64, regs ...func(*Webservice)) *httptest.Server {
	t.Helper()
	ws := NewWebservice(WebserviceConfig{
		DbPath:          t.TempDir(),
		Auth:            auth.NewStore(nil, auth.RoleWrite),
		MaxRequestBytes: maxBytes,
	})
	for _, reg := range regs {
		reg(ws)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeStatus(t *testing.T, resp *http.Response) ReplyStatus {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	var st ReplyStatus
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("the error body must be valid JSON, got %q: %v", body, err)
	}
	return st
}

// TestUnknownRequestIs404 is the §4.15 guard: an unrecognized request id used
// to answer 200 with an empty body, which the client then failed to decode —
// and which left proxies and monitoring blind to the failure.
func TestUnknownRequestIs404(t *testing.T) {
	srv := handlerServer(t, 0)
	resp := post(t, srv.URL+"/NoSuchRequest", "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown request id: status = %d, want 404", resp.StatusCode)
	}
	st := decodeStatus(t, resp)
	if st.Success {
		t.Error("an unknown request must not report success")
	}
}

// TestOversizedBodyIsRejected: the body used to be read with no limit, so one
// oversized request exhausted memory.
func TestOversizedBodyIsRejected(t *testing.T) {
	srv := handlerServer(t, 64, func(ws *Webservice) {
		Register(ws, "test", auth.AccessWrite, testHandler)
	})
	resp := post(t, srv.URL+"/test", strings.Repeat("x", 4096))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", resp.StatusCode)
	}
	st := decodeStatus(t, resp)
	if st.Success {
		t.Error("an oversized body must not report success")
	}
	// A body inside the limit still works.
	resp2 := post(t, srv.URL+"/test", `{"Id":"test"}`)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("a body within the limit: status = %d, want 200", resp2.StatusCode)
	}
}

// TestApplicationErrorKeepsJSONBodyWithStatus: an application error used to be
// HTTP 200 with Success:false, which makes curl, proxies and monitoring blind.
// The status must say what happened and the JSON body must survive so the
// client can still report the message.
func TestApplicationErrorKeepsJSONBodyWithStatus(t *testing.T) {
	srv := handlerServer(t, 0,
		func(ws *Webservice) { Register(ws, "notfound", auth.AccessRead, failingHandler(ErrCollectionNotFound)) },
		func(ws *Webservice) {
			Register(ws, "internal", auth.AccessRead, failingHandler(errors.New("disk on fire")))
		},
	)

	resp := post(t, srv.URL+"/notfound", `{"Id":"notfound"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("ErrCollectionNotFound: status = %d, want 404", resp.StatusCode)
	}
	if st := decodeStatus(t, resp); st.Success || !strings.Contains(st.Message, "not found") {
		t.Errorf("body = %+v, want the not-found message", st)
	}

	resp = post(t, srv.URL+"/internal", `{"Id":"internal"}`)
	if resp.StatusCode < 500 || resp.StatusCode > 599 {
		t.Errorf("internal error: status = %d, want 5xx", resp.StatusCode)
	}
	if st := decodeStatus(t, resp); st.Success || !strings.Contains(st.Message, "disk on fire") {
		t.Errorf("body = %+v, want the server message", st)
	}
}

// TestErrorToJsonStringIsValid: the marshalling-failure fallback used to emit
// `["Success": false, ...]`, which is not JSON at all.
func TestErrorToJsonStringIsValid(t *testing.T) {
	got := ErrorToJsonString("Error:", errors.New("boom"))
	var st ReplyStatus
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		t.Fatalf("fallback is not valid JSON: %q: %v", got, err)
	}
	if st.Success || !strings.Contains(st.Message, "boom") {
		t.Errorf("fallback = %+v", st)
	}
	// A message containing quotes and control characters must still be valid.
	got = ErrorToJsonString("Error:", errors.New("quote \" and \n newline"))
	if !json.Valid([]byte(got)) {
		t.Errorf("not valid JSON: %q", got)
	}
}
