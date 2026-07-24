package webservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"
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

	// Users maps a username to its password. Access management is done by
	// populating this from the daemon's config file; it is read-only at runtime.
	Users map[string]string

	// ReverseRelations restricts which relations (value1) collections index in
	// reverse. Empty/nil indexes every relation (the original behavior). Set it
	// to just the relations queried in reverse to cut association memory roughly
	// in half on metadata-heavy stores.
	ReverseRelations []string

	// MaxConcurrentRequests caps how many requests execute simultaneously.
	// Zero or negative means unbounded. tiedb is concurrency-safe, so this is a
	// load-shedding knob, not a correctness requirement.
	MaxConcurrentRequests int
}

func NewWebservice(config WebserviceConfig, requests []RequestInterface) *Webservice {
	ws := &Webservice{}
	ws.mux = http.NewServeMux()
	ws.Config = config
	ws.requests = requests
	ws.db = tiedb.NewDB(true)
	ws.db.SetDefaultReverseRelations(config.ReverseRelations)
	ws.db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	if config.MaxConcurrentRequests > 0 {
		ws.sem = make(chan struct{}, config.MaxConcurrentRequests)
	}

	return ws
}

func (ws *Webservice) ListenAndServe(routes func(*http.ServeMux)) {
	routes(ws.mux)

	if !ws.Config.Insecure && (ws.Config.CertFile == "" || ws.Config.KeyFile == "") {
		fmt.Println("Error: set CertFile and KeyFile in the config file, or set Insecure = true.")
		fmt.Println("Exiting.")
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
		fmt.Println("\nShutting down: draining requests and syncing DB...")
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Println("tiedb: HTTP shutdown error:", err)
		}
		ws.Close()
		os.Exit(0)
	}()

	if ws.Config.Insecure {
		fmt.Println("Listening on http://" + ws.Config.ListenOn + "\n")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	} else {
		fmt.Println("Listening on https://" + ws.Config.ListenOn + "\n")
		if err := srv.ListenAndServeTLS(ws.Config.CertFile, ws.Config.KeyFile); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}

// Close flushes and closes every collection's disk writer. Call it on shutdown
// (e.g. from a SIGTERM handler) so the daemon durably persists its tail of
// writes rather than relying on the kernel to flush the page cache after exit.
func (ws *Webservice) Close() {
	ws.db.Close()
}

func (ws *Webservice) BasicAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get the Basic Authentication credentials
		user, password, hasAuth := r.BasicAuth()
		realPW, userExists := ws.GetPassword(user)
		if hasAuth && userExists && password == realPW {
			// Delegate request to the given handler
			h(w, r)
		} else {
			// Request Basic Authentication otherwise
			w.Header().Set("WWW-Authenticate", "Basic realm=Restricted")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		}
	}
}

func (ws *Webservice) GetPassword(user string) (password string, exists bool) {
	pw, ok := ws.Config.Users[user]

	return pw, ok
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

func (ws *Webservice) RequestHandler(w http.ResponseWriter, r *http.Request) {
	raw_data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	reqName := r.PathValue("request")
	log.Println("Request from " + r.RemoteAddr + ": " + reqName)
	username, _, _ := r.BasicAuth() // Credentials already validated

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

	log.Println("Unrecognized request from " + r.RemoteAddr + ": " + reqName)
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
		log.Println(errRequest)
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
			log.Println("stream reply error:", err)
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
