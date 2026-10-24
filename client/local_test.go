package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/uidbz/tie/api"
	ws "github.com/uidbz/tie/webservice"
)

// TestLocalTransportRoundTrip exercises the in-process transport the way the
// real client does: through Call/CallContext and a streamed Dump. It must work
// with no network at all and ignore the namespace (a local file has no
// namespace directory).
func TestLocalTransportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := newLocalTransport(dir)
	defer tr.Close()

	info := api.CollectionInfo{Namespace: "ignored", CollectionId: "Main"}

	add, err := ws.Call[*api.AddRequest, api.AddReply](tr, info.NewAddRequest("key1", "tag", "jazz"))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !add.Success {
		t.Fatalf("Add was not successful: %+v", add.ReplyStatus)
	}

	q := info.NewQueryRequest()
	q.Terms = []string{"jazz"}
	q.Reverse = true
	q.Expand = true
	q.Sort.Limit = -1
	reply, err := ws.Call[*api.QueryRequest, api.QueryReply](tr, q)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !reply.Success || len(reply.Rows) != 1 || reply.Rows[0].Key != "key1" {
		t.Fatalf("Query = %+v", reply)
	}

	// The dump stream must arrive incrementally and decode as StringTriples.
	dump := info.NewDumpRequest()
	body, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	var triples [][3]string
	err = tr.DoStream(context.Background(), dump.GetId(), body, func(r io.Reader) error {
		dec := json.NewDecoder(r)
		for dec.More() {
			var tr struct {
				Key, Value1, Value2 string
			}
			if err := dec.Decode(&tr); err != nil {
				return err
			}
			triples = append(triples, [3]string{tr.Key, tr.Value1, tr.Value2})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("DumpStream: %v", err)
	}
	if len(triples) != 1 || triples[0] != [3]string{"key1", "tag", "jazz"} {
		t.Fatalf("dump = %v", triples)
	}
}

// TestLocalTransportStreamErrorBeforeBody: a failure before the first record
// (an invalid collection name) must surface as the real status error, not as a
// JSON decode failure in the consumer.
func TestLocalTransportStreamErrorBeforeBody(t *testing.T) {
	tr := newLocalTransport(t.TempDir())
	defer tr.Close()

	info := api.CollectionInfo{Namespace: "ignored", CollectionId: "../escape"}
	dump := info.NewDumpRequest()
	body, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = tr.DoStream(context.Background(), dump.GetId(), body, func(r io.Reader) error {
		called = true
		// The reader yields the transport error; it must not look like an
		// empty, successful stream.
		var b [1]byte
		if _, rerr := r.Read(b[:]); rerr == nil {
			return nil
		}
		return nil
	})
	if err == nil {
		t.Fatal("an invalid collection name was accepted")
	}
	if !called {
		t.Error("the stream consumer was never invoked")
	}
	var se *ws.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T (%v), want *webservice.StatusError", err, err)
	}
	if !strings.Contains(se.Message, "invalid namespace/collection name") {
		t.Errorf("message = %q", se.Message)
	}
}

// TestLocalTransportUnknownRequest: an unregistered id reports 404, matching
// the HTTP handler rather than answering 200 with an empty body.
func TestLocalTransportUnknownRequest(t *testing.T) {
	tr := newLocalTransport(t.TempDir())
	defer tr.Close()

	_, err := tr.Do(context.Background(), "Nope", []byte("{}"))
	var se *ws.StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err = %v, want a 404 StatusError", err)
	}
}

// TestLocalTransportContext: a cancelled context is honored before any work.
func TestLocalTransportContext(t *testing.T) {
	tr := newLocalTransport(t.TempDir())
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tr.Do(ctx, api.IdDummy, []byte("{}")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
