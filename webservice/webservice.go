package webservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/tiedb"
)

type Webservice struct {
	Config   WebserviceConfig
	mux      *http.ServeMux
	requests []RequestInterface
	db       *tiedb.TieTree
	// sem bounds how many requests run concurrently. A nil channel means no
	// ceiling. Each request acquires a slot before Reply and releases it after.
	sem chan struct{}
}

type WebserviceConfig struct {
	ListenOn      string
	Insecure      bool
	CertFile      string
	KeyFile       string
	UserNamespace string
	DbPath        string

	// Auth resolves each request's role from its Basic Auth credentials and gates
	// read vs write operations. Access management is done by populating it from
	// the daemon's config file; it is read-only at runtime.
	Auth *auth.Store

	// ReverseRelations restricts which relations (value1) collections index in
	// reverse. Empty/nil indexes every relation (the original behavior). Set it
	// to just the relations queried in reverse to cut association memory roughly
	// in half on metadata-heavy stores. Acts as the default for collections with
	// no CollectionReverseRelations override.
	ReverseRelations []string

	// CollectionReverseRelations pins the reverse-relation set for specific
	// (namespace, collection) pairs, overriding ReverseRelations. A pair listed
	// here indexes exactly its relations in reverse; unlisted collections use the
	// default. Applied at collection load time, so changes require a restart.
	CollectionReverseRelations []CollectionReverseRelations

	// MaxConcurrentRequests caps how many requests execute simultaneously.
	// Zero or negative means unbounded. tiedb is concurrency-safe, so this is a
	// load-shedding knob, not a correctness requirement.
	MaxConcurrentRequests int
}

// CollectionReverseRelations overrides the reverse-relation set for one
// collection. Relations lists the value1 relations that collection indexes in
// reverse (replacing the WebserviceConfig.ReverseRelations default for it).
type CollectionReverseRelations struct {
	Namespace  string
	Collection string
	Relations  []string
}

func NewWebservice(config WebserviceConfig, requests []RequestInterface) *Webservice {
	ws := &Webservice{}
	ws.mux = http.NewServeMux()
	ws.Config = config
	ws.requests = requests
	ws.db = tiedb.NewDB(true)
	ws.db.SetDefaultReverseRelations(config.ReverseRelations)
	if len(config.CollectionReverseRelations) > 0 {
		overrides := make(map[tiedb.CollectionKey][]string, len(config.CollectionReverseRelations))
		for _, o := range config.CollectionReverseRelations {
			key := tiedb.CollectionKey{Database: ws.DbPath(o.Namespace), Collection: o.Collection}
			overrides[key] = o.Relations
		}
		ws.db.SetReverseRelationsOverrides(overrides)
	}
	ws.db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	if config.MaxConcurrentRequests > 0 {
		ws.sem = make(chan struct{}, config.MaxConcurrentRequests)
	}

	return ws
}

func (ws *Webservice) ListenAndServe(routes func(*http.ServeMux)) {
	routes(ws.mux)

	if !ws.Config.Insecure && (ws.Config.CertFile == "" || ws.Config.KeyFile == "") {
		slog.Error("set CertFile and KeyFile in the config file, or set Insecure = true; exiting")
		return
	}

	srv := &http.Server{Addr: ws.Config.ListenOn, Handler: ws.mux}

	// On SIGINT/SIGTERM, stop accepting connections and let in-flight handlers
	// finish (so any write they enqueue is captured), THEN close the DB. Ordering
	// matters: closing the DB before draining handlers could drop a request's
	// write that had not yet reached the writer's queue.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("shutting down: draining requests and syncing DB")
		if err := srv.Shutdown(context.Background()); err != nil {
			slog.Error("HTTP shutdown error", "err", err)
		}
		ws.Close()
		os.Exit(0)
	}()

	if ws.Config.Insecure {
		slog.Info("listening", "addr", "http://"+ws.Config.ListenOn)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Info("listening", "addr", "https://"+ws.Config.ListenOn)
		if err := srv.ListenAndServeTLS(ws.Config.CertFile, ws.Config.KeyFile); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}
}

// Close flushes and closes every collection's disk writer. Call it on shutdown
// (e.g. from a SIGTERM handler) so the daemon durably persists its tail of
// writes rather than relying on the kernel to flush the page cache after exit.
func (ws *Webservice) Close() {
	ws.db.Close()
}

// readRequests are the request Ids that only read state. Every other Id
// (including any future one) is treated as a write, so a new write operation is
// never accidentally exposed to a read-only user.
var readRequests = map[string]bool{
	"Dummy":      true,
	"Query":      true,
	"Expand":     true,
	"Associated": true,
	"CoTags":     true,
	"Dump":       true,
}

// accessFor classifies a request Id as read or write. Unknown Ids default to
// write (fail-safe).
func accessFor(reqName string) auth.Access {
	if readRequests[reqName] {
		return auth.AccessRead
	}
	return auth.AccessWrite
}

func (ws *Webservice) DbPath(namespace string) string {
	return filepath.Join(ws.Config.DbPath, namespace)
}

func (ws *Webservice) GetAccount(account string) *tiedb.Collection {
	key := tiedb.CollectionKey{Database: ws.DbPath(ws.Config.UserNamespace), Collection: account}

	return ws.db.GetCollection(key)
}

func (ws *Webservice) GetCollection(namespace, collection string) *tiedb.Collection {
	key := tiedb.CollectionKey{Database: ws.DbPath(namespace), Collection: collection}

	return ws.db.GetCollection(key)
}

func (ws *Webservice) DropCollection(namespace, collection string) error {
	key := tiedb.CollectionKey{Database: ws.DbPath(namespace), Collection: collection}

	return ws.db.DropCollection(key)
}

func (ws *Webservice) RequestHandler(w http.ResponseWriter, r *http.Request) {
	raw_data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	reqName := r.PathValue("request")
	slog.Info("request", "from", r.RemoteAddr, "request", reqName)

	if ok, status := ws.Config.Auth.Authorize(r, accessFor(reqName)); !ok {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		}
		http.Error(w, http.StatusText(status), status)
		slog.Warn("access denied", "from", r.RemoteAddr, "request", reqName, "status", status)
		return
	}
	username, _, _ := r.BasicAuth()

	for _, x := range ws.requests {
		if reqName == x.GetId() {
			// Unmarshal into a fresh instance so concurrent requests never
			// share the prototype's mutable fields. The HTTP server already
			// runs each request on its own goroutine, so we answer inline.
			if ws.sem != nil {
				ws.sem <- struct{}{}
				defer func() { <-ws.sem }()
			}
			ws.AnswerRequest(w, x.New(), raw_data, username)
			return
		}
	}

	slog.Warn("unrecognized request", "from", r.RemoteAddr, "request", reqName)
}

func ErrorToJsonString(prepend string, err error) string {
	msg := ReplyStatus{
		Success: false,
		Message: prepend + " " + err.Error(),
	}
	json_reply, err := json.Marshal(msg)

	if err != nil {
		return "[\"Success\": false, \"Message\": \"Internal error: " + err.Error() + "\"]"
	}

	return string(json_reply)
}

func (ws *Webservice) AnswerRequest(w http.ResponseWriter, request RequestInterface, rawData []byte, username string) {
	errRequest := json.Unmarshal(rawData, request)
	if errRequest != nil {
		slog.Error("unmarshalling request", "err", errRequest)
		msg := ErrorToJsonString("Error unmarshalling request:", errRequest)
		fmt.Fprint(w, msg)
		return
	}

	env := &Environment{username, ws.GetAccount, ws.GetCollection, ws}

	// Streaming requests write their reply directly to w (NDJSON) so the whole
	// reply is never held in memory. The caller's semaphore slot (see
	// RequestHandler) is held for the full stream — that is the intended
	// MaxConcurrentRequests load-shedding behavior for a large dump.
	if s, ok := request.(StreamingRequestInterface); ok {
		w.Header().Set("Content-Type", "application/x-ndjson")
		if err := s.StreamReply(env, w); err != nil {
			// Status and headers are already committed; we can only log.
			slog.Error("stream reply error", "err", err)
		}
		return
	}

	reply, errReply := request.Reply(env)
	if errReply != nil {
		msg := ErrorToJsonString("Error:", errReply)
		fmt.Fprint(w, msg)
	} else {
		json_reply, errMarshal := json.Marshal(reply.ReplyStructPtr)
		if errMarshal != nil {
			msg := ErrorToJsonString("Internal error:", errMarshal)
			fmt.Fprint(w, msg)
		} else {
			fmt.Fprint(w, string(json_reply))
		}
	}
}
