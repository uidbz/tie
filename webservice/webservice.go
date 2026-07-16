package webservice

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sync"

	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"
	"github.com/caddyserver/certmagic"
)

type Webservice struct {
	Config           WebserviceConfig
	mux              *http.ServeMux
	requests         []RequestInterface
	db               *tiedb.TieTree
	requestsToAnswer chan *RequestToAnswer
}

type RequestToAnswer struct {
	AnswerTo http.ResponseWriter
	RawData  []byte
	Request  RequestInterface
	Username string
	Wait     *sync.WaitGroup
}

type WebserviceConfig struct {
	ListenOn      string
	Insecure      bool
	CertFile      string
	KeyFile       string
	UseCertmagic  bool
	CertmagicHost string
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
}

func NewWebservice(config WebserviceConfig, requests []RequestInterface) *Webservice {
	ws := &Webservice{}
	ws.mux = http.NewServeMux()
	ws.Config = config
	ws.requests = requests
	ws.db = tiedb.NewDB(true)
	ws.db.SetDefaultReverseRelations(config.ReverseRelations)
	ws.db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	ws.requestsToAnswer = make(chan *RequestToAnswer, 10000)
	ws.startRequestAnswerer()

	return ws
}

func (ws *Webservice) ListenAndServe(routes func(*http.ServeMux)) {
	routes(ws.mux)

	if ws.Config.Insecure {
		fmt.Println("Listening on http://" + ws.Config.ListenOn + "\n")
		log.Fatal(http.ListenAndServe(ws.Config.ListenOn, ws.mux))
	} else {
		if ws.Config.UseCertmagic {
			log.Fatal(certmagic.HTTPS([]string{ws.Config.CertmagicHost}, ws.mux))
		} else {
			if ws.Config.CertFile == "" || ws.Config.KeyFile == "" {
				fmt.Println("Error: Please provide --tls-cert <file.crt> and --tls-key <file.key> or set --insecure.")
				fmt.Println("Exiting.")
				return
			}
			fmt.Println("Listening on https://" + ws.Config.ListenOn + "\n")
			log.Fatal(http.ListenAndServeTLS(ws.Config.ListenOn, ws.Config.CertFile, ws.Config.KeyFile, ws.mux))
		}
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
	key := tiedb.CollectionKey{ws.DbPath(ws.Config.UserNamespace), account}

	return ws.db.GetCollection(key)
}

func (ws *Webservice) GetCollection(namespace, collection string) *tiedb.Collection {
	key := tiedb.CollectionKey{ws.DbPath(namespace), collection}

	return ws.db.GetCollection(key)
}

func (ws *Webservice) RequestHandler(w http.ResponseWriter, r *http.Request) {
	raw_data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	reqName := r.PathValue("request")
	log.Println("Request from " + r.RemoteAddr + ": " + reqName)
	username, _, _ := r.BasicAuth() // Credentials already validated

	req := &RequestToAnswer{
		AnswerTo: w,
		RawData:  raw_data,
		Username: username,
		Wait:     &sync.WaitGroup{},
	}

	for _, x := range ws.requests {
		if reqName == x.GetId() {
			req.Request = x
			req.Wait.Add(1)
			ws.requestsToAnswer <- req
			req.Wait.Wait() // Wait until request is answered otherwise ResponseWriter will be closed
			return
		}
	}

	log.Println("Unrecognized request from " + r.RemoteAddr + ": " + reqName)
}

func (ws *Webservice) startRequestAnswerer() {
	go func() {
		for req := range ws.requestsToAnswer {
			ws.AnswerRequest(req)
			req.Wait.Done()
		}
	}()
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

func (ws *Webservice) AnswerRequest(req *RequestToAnswer) {
	errRequest := json.Unmarshal(req.RawData, req.Request)
	if errRequest != nil {
		log.Println(errRequest)
		msg := ErrorToJsonString("Error unmarshalling request:", errRequest)
		fmt.Fprint(req.AnswerTo, msg)
		return
	}

	reply, errReply := req.Request.Reply(&Environment{req.Username, ws.GetAccount, ws.GetCollection, ws})
	if errReply != nil {
		msg := ErrorToJsonString("Error:", errReply)
		fmt.Fprint(req.AnswerTo, msg)
	} else {
		json_reply, errMarshal := json.Marshal(reply.ReplyStructPtr)
		if errMarshal != nil {
			msg := ErrorToJsonString("Internal error:", errMarshal)
			fmt.Fprint(req.AnswerTo, msg)
		} else {
			fmt.Fprint(req.AnswerTo, string(json_reply))
		}
	}
}
