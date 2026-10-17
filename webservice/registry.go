package webservice

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/auth"
)

// A request type used to be a file copied from a template: the struct, a Reply
// method, a New() prototype and a constructor — some forty lines, sixteen times
// over, with the read/write classification kept separately in a map that a new
// request could silently fall out of. The copy-paste is how the same field
// assignment came to be wrong in three files (§7.5).
//
// The registry owns what was duplicated: decoding into a fresh request, the
// access check, encoding the reply and the status codes. A request type is now
// its struct plus a handler function, and registering it states its access
// level in the same line.

// Handler answers one request. Returning an error answers the client with the
// status statusForError assigns to it; the reply value is then ignored.
type Handler[Req, Rep any] func(env *Environment, req *Req) (*Rep, error)

// StreamHandler writes its reply directly to w (NDJSON, one record per line) so
// neither the server nor the client holds the whole reply in memory. Once the
// first byte is written the HTTP status is committed, so a mid-stream error can
// only be logged.
type StreamHandler[Req any] func(env *Environment, req *Req, w io.Writer) error

// registration is a handler with the access level it requires, type-erased so
// the registry can hold handlers for many request types in one map.
type registration struct {
	access auth.Access
	serve  func(env *Environment, raw []byte, w http.ResponseWriter)
}

// Register installs one request type. The handler's argument and result types
// decide how the request is decoded and what the reply is, so there is no
// prototype to keep in sync and no way to register a handler under the wrong
// request type: that is a compile error.
func Register[Req, Rep any](ws *Webservice, id string, access auth.Access, h Handler[Req, Rep]) {
	ws.register(id, registration{
		access: access,
		serve: func(env *Environment, raw []byte, w http.ResponseWriter) {
			req := new(Req)
			if err := json.Unmarshal(raw, req); err != nil {
				slog.Error("unmarshalling request", "request", id, "err", err)
				answerError(w, http.StatusBadRequest, "Error unmarshalling request:", err)
				return
			}
			rep, err := h(env, req)
			if err != nil {
				answerError(w, statusForError(err), "Error:", err)
				return
			}
			body, err := json.Marshal(rep)
			if err != nil {
				// The reply never reached the wire, so a 500 with a decodable
				// body is honest.
				answerError(w, http.StatusInternalServerError, "Internal error:", err)
				return
			}
			fmt.Fprint(w, string(body))
		},
	})
}

// RegisterStream installs a request type whose reply is streamed.
func RegisterStream[Req any](ws *Webservice, id string, access auth.Access, h StreamHandler[Req]) {
	ws.register(id, registration{
		access: access,
		serve: func(env *Environment, raw []byte, w http.ResponseWriter) {
			req := new(Req)
			if err := json.Unmarshal(raw, req); err != nil {
				slog.Error("unmarshalling request", "request", id, "err", err)
				answerError(w, http.StatusBadRequest, "Error unmarshalling request:", err)
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			if err := h(env, req, w); err != nil {
				// Status and headers are already committed; logging is all
				// that is left.
				slog.Error("stream reply error", "request", id, "err", err)
			}
		},
	})
}
