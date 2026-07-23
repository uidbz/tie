package webservice

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"

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

	if ws.Config.Insecure {
		fmt.Println("Listening on http://" + ws.Config.ListenOn + "\n")
		log.Fatal(http.ListenAndServe(ws.Config.ListenOn, ws.mux))
	} else {
		if ws.Config.CertFile == "" || ws.Config.KeyFile == "" {
			fmt.Println("Error: set CertFile and KeyFile in the config file, or set Insecure = true.")
			fmt.Println("Exiting.")
			return
		}
		fmt.Println("Listening on https://" + ws.Config.ListenOn + "\n")
		log.Fatal(http.ListenAndServeTLS(ws.Config.ListenOn, ws.Config.CertFile, ws.Config.KeyFile, ws.mux))
	}
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

	reply, errReply := request.Reply(&Environment{username, ws.GetAccount, ws.GetCollection, ws})
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
