package webservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/tiedb"
)

type Webservice struct {
	Config WebserviceConfig
	mux    *http.ServeMux
	// handlers maps a request id to its handler and access level. Registering
	// a request states its access, so a new request cannot fall out of a
	// separate read/write table (§7.5).
	handlers map[string]registration
	db       *tiedb.TieTree
	// sem bounds how many requests run concurrently. A nil channel means no
	// ceiling. Each request acquires a slot before Reply and releases it after.
	sem chan struct{}
}

type WebserviceConfig struct {
	ListenOn string
	Insecure bool
	CertFile string
	KeyFile  string
	DbPath   string

	// Auth resolves each request's role from its Basic Auth credentials and gates
	// read vs write operations. Access management is done by populating it from
	// the server's config file; it is read-only at runtime.
	Auth *auth.Store

	// MaxRequestBytes caps the body of a single request. Restore batches are
	// legitimately large, so the default is generous; the point is that the
	// limit exists at all, because the body is read whole before it is parsed.
	// Zero selects defaultMaxRequestBytes.
	MaxRequestBytes int64

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

	// LoadMemoryLimitMB is a soft heap ceiling (GOMEMLIMIT semantics) applied
	// while a collection is loaded from disk, bounding the peak a relaxed load
	// pacer can otherwise reach on a small machine. 0 = runtime default.
	LoadMemoryLimitMB int

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

// NewWebservice builds an empty service: it knows no request types until they
// are registered on it (api.RegisterAll installs the tie protocol).
func NewWebservice(config WebserviceConfig) *Webservice {
	ws := &Webservice{}
	ws.mux = http.NewServeMux()
	ws.Config = config
	ws.handlers = map[string]registration{}
	ws.db = tiedb.NewDB(true)
	ws.db.SetDefaultReverseRelations(config.ReverseRelations)
	if config.LoadMemoryLimitMB > 0 {
		ws.db.SetLoadMemoryLimit(int64(config.LoadMemoryLimitMB) * 1024 * 1024)
	}
	if len(config.CollectionReverseRelations) > 0 {
		overrides := make(map[tiedb.CollectionKey][]string, len(config.CollectionReverseRelations))
		for _, o := range config.CollectionReverseRelations {
			key := tiedb.CollectionKey{Database: ws.DbPath(o.Namespace), Collection: o.Collection}
			overrides[key] = o.Relations
		}
		ws.db.SetReverseRelationsOverrides(overrides)
	}
	ws.db.SetBlobPolicy(tiedb.HexHashBlobPolicy(metadata.IsHexHash))
	if config.MaxConcurrentRequests > 0 {
		ws.sem = make(chan struct{}, config.MaxConcurrentRequests)
	}

	return ws
}

// ListenAndServe serves the API until the process is asked to stop. It returns
// an error rather than exiting: whether a failure kills the process is main's
// call (cmd/tie-triplestore), not a library's. On SIGINT/SIGTERM it drains
// in-flight handlers, closes the DB, and returns nil.
func (ws *Webservice) ListenAndServe(routes func(*http.ServeMux)) error {
	routes(ws.mux)

	if !ws.Config.Insecure && (ws.Config.CertFile == "" || ws.Config.KeyFile == "") {
		return errors.New("set CertFile and KeyFile in the config file, or set Insecure = true")
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
	}()

	if ws.Config.Insecure {
		slog.Info("listening", "addr", "http://"+ws.Config.ListenOn)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("listening on %s: %w", ws.Config.ListenOn, err)
		}
		return nil
	}
	slog.Info("listening", "addr", "https://"+ws.Config.ListenOn)
	if err := srv.ListenAndServeTLS(ws.Config.CertFile, ws.Config.KeyFile); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listening on %s: %w", ws.Config.ListenOn, err)
	}
	return nil
}

// Close flushes and closes every collection's disk writer. Call it on shutdown
// (e.g. from a SIGTERM handler) so the server durably persists its tail of
// writes rather than relying on the kernel to flush the page cache after exit.
func (ws *Webservice) Close() {
	ws.db.Close()
}

// AccessFor classifies a request id as read or write from how it was
// registered. An unknown id defaults to write (fail-safe), so a request that was
// never registered cannot be reached with read-only credentials.
func (ws *Webservice) AccessFor(reqName string) auth.Access {
	if reg, ok := ws.handlers[reqName]; ok {
		return reg.access
	}
	return auth.AccessWrite
}

// register installs a request type. Register and RegisterStream are the
// type-safe front ends; this is the shared part.
func (ws *Webservice) register(id string, reg registration) {
	if ws.handlers == nil {
		ws.handlers = map[string]registration{}
	}
	ws.handlers[id] = reg
}

// ErrCollectionNotFound is returned by the read-path collection resolution
// when the collection does not exist. Reads must not create collections:
// any read-role caller (anonymous by default) could otherwise mint an
// unbounded number of them — a file, directory, writer goroutine and
// channel buffers per name — just by querying random names.
var ErrCollectionNotFound = errors.New("collection not found")

// ErrInvalidName marks a request-supplied namespace or collection id that the
// name pattern rejects. It reaches the HTTP layer as 400 rather than 500, so a
// client bug or an attack is distinguishable from a server fault in logs and
// monitoring.
var ErrInvalidName = errors.New("invalid namespace/collection name")

// defaultMaxRequestBytes caps a request body when the config does not. A body
// is held whole in memory before it is parsed, so the cap has to exist; it is
// generous because a Batch (restore, repair) legitimately carries many ops.
const defaultMaxRequestBytes = 256 << 20

func (ws *Webservice) maxRequestBytes() int64 {
	if ws.Config.MaxRequestBytes > 0 {
		return ws.Config.MaxRequestBytes
	}
	return defaultMaxRequestBytes
}

// statusForError maps an application error to an HTTP status. Application
// errors used to be answered as 200 with Success:false, which leaves curl,
// proxies and monitoring blind to them (§4.15).
func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrCollectionNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidName):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// answerError writes an application error with a real status code and the JSON
// ReplyStatus body the client decodes, so the message still reaches it.
func answerError(w http.ResponseWriter, status int, prepend string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, ErrorToJsonString(prepend, err))
}

// collectionNamePattern bounds namespace and collection ids to name-safe
// characters. Both arrive straight from the request and are used to build
// filesystem paths (DbPath join + "<collection>.tie"); without this,
// Namespace: "../../home/x" creates or appends .tie files anywhere the
// service user can write, and Drop deletes them.
var collectionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validCollectionName(s string) bool { return collectionNamePattern.MatchString(s) }

func (ws *Webservice) DbPath(namespace string) string {
	return filepath.Join(ws.Config.DbPath, namespace)
}

// collectionKey validates the request-supplied namespace and collection id
// and builds the tiedb key. This is the single validation point for every
// request-supplied collection name.
func (ws *Webservice) collectionKey(namespace, collection string) (tiedb.CollectionKey, error) {
	if !validCollectionName(namespace) || !validCollectionName(collection) {
		return tiedb.CollectionKey{}, fmt.Errorf("%w %q/%q", ErrInvalidName, namespace, collection)
	}
	return tiedb.CollectionKey{Database: ws.DbPath(namespace), Collection: collection}, nil
}

// GetCollection resolves a request's collection. Write requests may create
// it; read requests only ever look it up, and get ErrCollectionNotFound for
// an absent one (handlers answer that like an empty collection).
func (ws *Webservice) GetCollection(namespace, collection string, writable bool) (*tiedb.Collection, error) {
	key, err := ws.collectionKey(namespace, collection)
	if err != nil {
		return nil, err
	}
	if writable {
		col, err := ws.db.TryGetCollection(key)
		if err != nil {
			return nil, err
		}
		return col, nil
	}
	col, ok := ws.db.LookupCollection(key)
	if !ok {
		return nil, ErrCollectionNotFound
	}
	if err := col.Err(); err != nil {
		return nil, err
	}
	return col, nil
}

func (ws *Webservice) DropCollection(namespace, collection string) error {
	key, err := ws.collectionKey(namespace, collection)
	if err != nil {
		return err
	}

	return ws.db.DropCollection(key)
}

func (ws *Webservice) RequestHandler(w http.ResponseWriter, r *http.Request) {
	// The body is read whole before it is parsed, so it must be capped: one
	// oversized request used to be enough to exhaust memory (§4.15).
	r.Body = http.MaxBytesReader(w, r.Body, ws.maxRequestBytes())
	raw_data, errRead := io.ReadAll(r.Body)
	r.Body.Close()
	reqName := r.PathValue("request")
	slog.Info("request", "from", r.RemoteAddr, "request", reqName)

	if ok, status := ws.Config.Auth.Authorize(r, ws.AccessFor(reqName)); !ok {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		}
		http.Error(w, http.StatusText(status), status)
		slog.Warn("access denied", "from", r.RemoteAddr, "request", reqName, "status", status)
		return
	}
	username, _, _ := r.BasicAuth()

	if errRead != nil {
		slog.Warn("reading request body", "from", r.RemoteAddr, "request", reqName, "err", errRead)
		answerError(w, http.StatusRequestEntityTooLarge, "Request body too large:", errRead)
		return
	}

	reg, known := ws.handlers[reqName]
	if known {
		// The handler decodes into a fresh request value, so concurrent
		// requests never share mutable state. The HTTP server already runs
		// each request on its own goroutine, so we answer inline.
		if ws.sem != nil {
			ws.sem <- struct{}{}
			defer func() { <-ws.sem }()
		}
		// Write requests may create the collection; reads only look it up.
		writable := reg.access == auth.AccessWrite
		env := &Environment{username, func(ns, col string) (*tiedb.Collection, error) {
			return ws.GetCollection(ns, col, writable)
		}, ws}
		reg.serve(env, raw_data, w)
		return
	}

	// An unknown request id used to answer 200 with an empty body, which the
	// client then failed to JSON-decode.
	slog.Warn("unrecognized request", "from", r.RemoteAddr, "request", reqName)
	answerError(w, http.StatusNotFound, "Unknown request:", fmt.Errorf("%q", reqName))
}

func ErrorToJsonString(prepend string, err error) string {
	msg := ReplyStatus{
		Success: false,
		Message: prepend + " " + err.Error(),
	}
	json_reply, err := json.Marshal(msg)

	if err != nil {
		// Marshal of ReplyStatus cannot realistically fail; if it does, the
		// fallback still has to be JSON the client can decode (the old one was
		// `["Success": false, ...]`, which is not JSON at all).
		fallback, _ := json.Marshal(ReplyStatus{
			Success: false,
			Message: "Internal error: " + err.Error(),
		})
		return string(fallback)
	}

	return string(json_reply)
}
