package webservice

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"
	"github.com/caddyserver/certmagic"
	"github.com/julienschmidt/httprouter"
)

type Webservice struct {
	Config           WebserviceConfig
	router           *httprouter.Router
	requests         []RequestInterface
	validationCodes  map[string]string
	validationTimers map[string]*time.Timer
	validationMutex  sync.Mutex
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

const (
	PASSWORDFIELD         = "password"
	MAILSETTINGSDB        = "mailsettingsdb"
	REQUESTVALIDATIONCODE = "requestvalidationcode"
	REQUESTCREATEACCOUNT  = "requestcreateaccount"
	VALIDATIONCHARS       = "0123456789"
	MAIL                  = "mail"     // Used for mail settings db
	SERVER                = "server"   // Used for mail settings db
	FROM                  = "from"     // Used for mail settings db
	SUBJECT               = "subject"  // Used for mail settings db
	USERNAME              = "username" // Used for mail settings db
	PASSWORD              = "password" // Used for mail settings db
	MESSAGE               = "message"  // Used for mail settings db
	VALIDATIONLENGTH      = 4
)

type WebserviceConfig struct {
	ListenOn      string
	Insecure      bool
	CertFile      string
	KeyFile       string
	UseCertmagic  bool
	CertmagicHost string
	AuthNamespace string
	UserNamespace string
	AuthFile      string
	authKey       tiedb.CollectionKey
	DbPath        string

	// ReverseRelations restricts which relations (value1) collections index in
	// reverse. Empty/nil indexes every relation (the original behavior). Set it
	// to just the relations queried in reverse to cut association memory roughly
	// in half on metadata-heavy stores.
	ReverseRelations []string
}

type MailSettings struct {
	Server   string
	From     string
	Subject  string
	Username string
	Password string
	Message  string
}

func NewWebservice(config WebserviceConfig, requests []RequestInterface) *Webservice {
	ws := &Webservice{}
	ws.router = httprouter.New()
	ws.Config = config
	ws.Config.authKey = tiedb.CollectionKey{ws.DbPath(config.AuthNamespace), config.AuthFile}
	ws.requests = requests
	ws.db = tiedb.NewDB(true)
	ws.db.SetDefaultReverseRelations(config.ReverseRelations)
	ws.db.SetBlobPolicy(metadata.HexHashBlobPolicy())
	ws.validationCodes = make(map[string]string)
	ws.validationTimers = make(map[string]*time.Timer)
	rand.Seed(time.Now().UnixNano())
	ws.requestsToAnswer = make(chan *RequestToAnswer, 10000)
	ws.startRequestAnswerer()

	return ws
}

func (ws *Webservice) ListenAndServe(routes func(*httprouter.Router)) {
	routes(ws.router)

	if ws.Config.Insecure {
		fmt.Println("Listening on http://" + ws.Config.ListenOn + "\n")
		log.Fatal(http.ListenAndServe(ws.Config.ListenOn, ws.router))
	} else {
		if ws.Config.UseCertmagic {
			log.Fatal(certmagic.HTTPS([]string{ws.Config.CertmagicHost}, ws.router))
		} else {
			if ws.Config.CertFile == "" || ws.Config.KeyFile == "" {
				fmt.Println("Error: Please provide --tls-cert <file.crt> and --tls-key <file.key> or set --insecure.")
				fmt.Println("Exiting.")
				return
			}
			fmt.Println("Listening on https://" + ws.Config.ListenOn + "\n")
			log.Fatal(http.ListenAndServeTLS(ws.Config.ListenOn, ws.Config.CertFile, ws.Config.KeyFile, ws.router))
		}
	}
}

func (ws *Webservice) BasicAuth(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// Get the Basic Authentication credentials
		user, password, hasAuth := r.BasicAuth()
		realPW, userExists := ws.GetPassword(user)
		if hasAuth && userExists && password == realPW {
			// Delegate request to the given handle
			switch user {
			case REQUESTVALIDATIONCODE:
				raw_data, _ := io.ReadAll(r.Body)
				valid := NewValidationCodeRequest()
				valid.getValidationCode = ws.GetValidationCode
				ws.AnswerRequest(&RequestToAnswer{w, raw_data, valid, user, nil})
			case REQUESTCREATEACCOUNT:
				raw_data, _ := io.ReadAll(r.Body)
				account := NewCreateAccountRequest()
				account.validate = ws.Validate
				account.addUser = ws.AddUser
				ws.AnswerRequest(&RequestToAnswer{w, raw_data, account, user, nil})
			default:
				h(w, r, ps)
			}
		} else {
			// Request Basic Authentication otherwise
			w.Header().Set("WWW-Authenticate", "Basic realm=Restricted")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		}
	}
}

func (ws *Webservice) GetPassword(user string) (password string, exists bool) {
	if user == REQUESTVALIDATIONCODE || user == REQUESTCREATEACCOUNT {
		return "", true
	}
	col := ws.db.GetCollection(ws.Config.authKey)
	userdata, userExists := col.Get(user, PASSWORDFIELD)
	pw := ""
	if userExists {
		pw, _ = userdata[user][PASSWORDFIELD].One()
		// if password == "" then do something appropriate
	}

	return pw, userExists
}

func RandomCode() string {
	b := make([]byte, VALIDATIONLENGTH)
	for i := range b {
		b[i] = VALIDATIONCHARS[rand.Intn(len(VALIDATIONCHARS))]
	}

	return string(b)
}

func (ws *Webservice) GetValidationCode(user string) string {
	ws.validationMutex.Lock()
	defer ws.validationMutex.Unlock()

	code := RandomCode()
	ws.validationCodes[user] = code
	timeout := 4 * time.Hour

	if _, exists := ws.validationTimers[user]; exists {
		ws.validationTimers[user].Reset(timeout)
	} else {
		ws.validationTimers[user] = time.NewTimer(timeout)
		go func() {
			<-ws.validationTimers[user].C
			ws.validationMutex.Lock()
			defer ws.validationMutex.Unlock()
			if _, ok := ws.validationCodes[user]; ok {
				delete(ws.validationCodes, user)
			}
			delete(ws.validationTimers, user)
		}()
	}

	return code
}

func (ws *Webservice) Validate(user, code string) bool {
	ws.validationMutex.Lock()
	defer ws.validationMutex.Unlock()

	if realcode, exists := ws.validationCodes[user]; exists {
		if code == realcode {
			delete(ws.validationCodes, user)

			return true
		}
	}

	return false
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

func (ws *Webservice) AddUser(username, password string) {
	col := ws.db.GetCollection(ws.Config.authKey)
	col.Add(username, PASSWORDFIELD, password)
}

func (ws *Webservice) DelUser(username, password string) bool {
	col := ws.db.GetCollection(ws.Config.authKey)
	_, success := col.Delete(username, PASSWORDFIELD, password)

	return success
}

func (ws *Webservice) UpdatePassword(username, password, newpassword string) bool {
	col := ws.db.GetCollection(ws.Config.authKey)
	_, success := col.Update(username, PASSWORDFIELD, password, newpassword)

	return success
}

func (ws *Webservice) SetMailSettings(mail MailSettings) {
	key := tiedb.CollectionKey{ws.Config.AuthNamespace, MAILSETTINGSDB}
	col := ws.db.GetCollection(key)

	col.SimpleUpdate(MAIL, SERVER, mail.Server, true)
	col.SimpleUpdate(MAIL, FROM, mail.From, true)
	col.SimpleUpdate(MAIL, SUBJECT, mail.Subject, true)
	col.SimpleUpdate(MAIL, USERNAME, mail.Username, true)
	col.SimpleUpdate(MAIL, PASSWORD, mail.Password, true)
	col.SimpleUpdate(MAIL, MESSAGE, mail.Message, true)
}

func (ws *Webservice) RequestHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	raw_data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	reqName := ps.ByName("request")
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

	log.Println("Unrecognized request from " + r.RemoteAddr + ": " + ps.ByName("type"))
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

// func (ws *Webservice) AnswerRequest(w http.ResponseWriter, raw_data []byte, r RequestInterface, username string) {
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
