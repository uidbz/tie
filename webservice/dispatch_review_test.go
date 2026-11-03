package webservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uidbz/tie/auth"
)

// Tests for the 2026-10 direct-.tie review findings on the in-process entry
// points: panic recovery, the request-size cap and context handling (#149),
// and LocalDir reverse-relation overrides (#157).

type probeRequest struct{ Value string }
type probeReply struct {
	Found bool
	Size  int
}

// TestDispatchRecoversHandlerPanic: the HTTP path runs each handler under
// net/http's per-request recover; Dispatch runs on the caller's goroutine,
// where a panic used to unwind the process (a corrupt direct file panics
// inside tiedb.loadPass, skipping tie -f's Close entirely) (#149).
func TestDispatchRecoversHandlerPanic(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{})
	Register(ws, "boom", auth.AccessRead, func(_ *Environment, _ *probeRequest) (*probeReply, error) {
		panic("test panic")
	})

	var buf bytes.Buffer
	status := ws.Dispatch("boom", []byte(`{}`), &buf)
	if status != http.StatusInternalServerError {
		t.Fatalf("panicking handler returned %d, want 500", status)
	}
	var reply ReplyStatus
	if err := json.Unmarshal(buf.Bytes(), &reply); err != nil {
		t.Fatalf("error body not decodable: %v (%q)", err, buf.String())
	}
	if reply.Success || !strings.Contains(reply.Message, "panic") {
		t.Errorf("error body = %+v, want a failure mentioning the panic", reply)
	}

	// The service still serves after the panic: recovery is per request.
	var buf2 bytes.Buffer
	if status := ws.Dispatch("unknown-id", []byte(`{}`), &buf2); status != http.StatusNotFound {
		t.Errorf("second dispatch returned %d, want 404 (service alive)", status)
	}
}

// TestDispatchStreamRecoversHandlerPanic covers the streaming path, where a
// panic after the first byte leaves the partial body (started=true) and
// reports the 500 alongside it (#149).
func TestDispatchStreamRecoversHandlerPanic(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{})
	RegisterStream(ws, "boom-late", auth.AccessRead,
		func(_ *Environment, _ *probeRequest, w io.Writer) error {
			_, _ = io.WriteString(w, `{"partial":true}`+"\n")
			panic("test stream panic")
		})
	RegisterStream(ws, "boom-early", auth.AccessRead,
		func(_ *Environment, _ *probeRequest, _ io.Writer) error {
			panic("test stream panic before body")
		})

	var buf bytes.Buffer
	started, err := ws.DispatchStream(context.Background(), "boom-late", []byte(`{}`), &buf)
	var se *StatusError
	if !started || !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Errorf("late panic = started %v, err %v; want started + 500", started, err)
	}
	if !strings.Contains(buf.String(), "partial") {
		t.Errorf("partial body lost: %q", buf.String())
	}

	started, err = ws.DispatchStream(context.Background(), "boom-early", []byte(`{}`), io.Discard)
	if started || !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Errorf("early panic = started %v, err %v; want pre-body 500", started, err)
	}
}

// TestDispatchHonoursRequestSizeCap: the HTTP path wraps the body in
// MaxBytesReader; the in-process path used to accept any size (#149).
func TestDispatchHonoursRequestSizeCap(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{MaxRequestBytes: 64})
	Register(ws, "echo", auth.AccessRead, func(_ *Environment, req *probeRequest) (*probeReply, error) {
		return &probeReply{Found: req.Value != ""}, nil
	})

	raw := bytes.Repeat([]byte("x"), 100) // oversized; never even decoded
	var buf bytes.Buffer
	if status := ws.Dispatch("echo", raw, &buf); status != http.StatusRequestEntityTooLarge {
		t.Errorf("Dispatch with a 100-byte body under a 64-byte cap: %d, want 413", status)
	}
	_, err := ws.DispatchStream(context.Background(), "echo", raw, io.Discard)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("DispatchStream oversize err = %v, want a 413 StatusError", err)
	}

	// A body within the cap still works.
	var buf2 bytes.Buffer
	if status := ws.Dispatch("echo", []byte(`{"Value":"ok"}`), &buf2); status != http.StatusOK {
		t.Errorf("in-cap dispatch returned %d, want 200", status)
	}
}

// TestDispatchStreamHonoursContext: a request whose context is already done
// must fail immediately, like an HTTP request whose connection went away.
func TestDispatchStreamHonoursContext(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{})
	RegisterStream(ws, "never", auth.AccessRead,
		func(_ *Environment, _ *probeRequest, w io.Writer) error {
			_, _ = io.WriteString(w, "ran\n")
			return nil
		})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started, err := ws.DispatchStream(ctx, "never", []byte(`{}`), io.Discard)
	if started || !errors.Is(err, context.Canceled) {
		t.Errorf("pre-cancelled stream = started %v, err %v; want context.Canceled", started, err)
	}
}

// TestDispatchStreamFallbackKeepsHandlerError pins #155: the non-stream
// fallback used to answer with http.StatusText (throwing away the handler's
// JSON error message) and pushed the error body into the consumer's record
// stream. The message must survive, and the error body must stay buffered.
func TestDispatchStreamFallbackKeepsHandlerError(t *testing.T) {
	ws := NewWebservice(WebserviceConfig{})
	Register(ws, "appfail", auth.AccessRead,
		func(_ *Environment, _ *probeRequest) (*probeReply, error) {
			return nil, errors.New("collection not found")
		})
	Register(ws, "okreply", auth.AccessRead,
		func(_ *Environment, req *probeRequest) (*probeReply, error) {
			return &probeReply{Found: true, Size: 7}, nil
		})

	var buf bytes.Buffer
	started, err := ws.DispatchStream(context.Background(), "appfail", []byte(`{}`), &buf)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want StatusError", err)
	}
	if started || buf.Len() != 0 {
		t.Errorf("error body leaked to the consumer: started %v body %q", started, buf.String())
	}
	if !strings.Contains(se.Message, "collection not found") {
		t.Errorf("message = %q, want the handler's own error text", se.Message)
	}

	// A successful non-stream handler on the stream entry point still has its
	// reply delivered, buffered then copied.
	buf.Reset()
	started, err = ws.DispatchStream(context.Background(), "okreply", []byte(`{}`), &buf)
	if started != true || err != nil {
		t.Fatalf("okreply = started %v, err %v", started, err)
	}
	var reply probeReply
	if err := json.Unmarshal(buf.Bytes(), &reply); err != nil || reply.Size != 7 {
		t.Errorf("okreply body = %q (%v), want the JSON reply", buf.String(), err)
	}
}

// TestLocalReverseRelationsOverrideMatchesLocalDir pins #157: NewWebservice
// used to key CollectionReverseRelations through DbPath(namespace) even in
// LocalDir mode, while local collections are keyed {LocalDir, name} — so a
// local override could never match and the collection silently indexed
// everything. The namespace must be ignored in local mode, as collectionKey
// ignores it.
func TestLocalReverseRelationsOverrideMatchesLocalDir(t *testing.T) {
	dir := t.TempDir()

	build := func(overrides []CollectionReverseRelations) *Webservice {
		ws := NewWebservice(WebserviceConfig{LocalDir: dir, CollectionReverseRelations: overrides})
		Register(ws, "write", auth.AccessWrite, func(env *Environment, req *probeRequest) (*probeReply, error) {
			col, err := env.Collection("Collections", "coll")
			if err != nil {
				return nil, err
			}
			if err := col.Add("k", req.Value, "jazz"); err != nil {
				return nil, err
			}
			col.Sync()
			return &probeReply{}, nil
		})
		Register(ws, "revprobe", auth.AccessRead, func(env *Environment, req *probeRequest) (*probeReply, error) {
			col, err := env.Collection("Collections", "coll")
			if err != nil {
				return nil, err
			}
			set, ok := col.GetReverseAssociations(req.Value)
			if !ok {
				return &probeReply{}, nil
			}
			return &probeReply{Found: true, Size: set.Size()}, nil
		})
		return ws
	}

	// Control: without an override the relation is reverse-indexed.
	plain := build(nil)
	raw, _ := json.Marshal(probeRequest{Value: "tag"})
	if status := plain.Dispatch("write", raw, io.Discard); status != http.StatusOK {
		t.Fatalf("seed write status = %d", status)
	}
	probe, _ := json.Marshal(probeRequest{Value: "jazz"})
	var buf bytes.Buffer
	if status := plain.Dispatch("revprobe", probe, &buf); status != http.StatusOK {
		t.Fatalf("probe status = %d", status)
	}
	var reply probeReply
	if err := json.Unmarshal(buf.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Size == 0 {
		t.Fatal("control collection did not reverse-index tag:jazz")
	}
	plain.Close() // release the file lock for the second service

	// With the override naming a different relation, tag is NOT indexed: the
	// override key must be {LocalDir, coll} so the override actually applies.
	restricted := build([]CollectionReverseRelations{
		{Namespace: "Collections", Collection: "coll", Relations: []string{"not-tag"}},
	})
	defer restricted.Close()
	if status := restricted.Dispatch("write", raw, io.Discard); status != http.StatusOK {
		t.Fatalf("override write status = %d", status)
	}
	buf.Reset()
	if status := restricted.Dispatch("revprobe", probe, &buf); status != http.StatusOK {
		t.Fatalf("override probe status = %d", status)
	}
	reply = probeReply{}
	if err := json.Unmarshal(buf.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Size != 0 {
		t.Errorf("override not applied: jazz still reverse-indexed (size %d)", reply.Size)
	}
}
