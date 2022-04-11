package webservice

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~uid/tie/tiedb"
	"github.com/caddyserver/certmagic"
	"github.com/julienschmidt/httprouter"
)

type Webservice struct {
	router           *httprouter.Router
	config           WebserviceConfig
	requests         []RequestInterface
	validationCodes  map[string]string
	validationTimers map[string]*time.Timer
	validationMutex  sync.Mutex
	db               *tiedb.Tree
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
	ws.config = config
	ws.config.authKey = tiedb.CollectionKey{config.AuthNamespace, config.AuthFile}
	ws.requests = requests
	ws.db = tiedb.NewDB(true)
	ws.validationCodes = make(map[string]string)
	ws.validationTimers = make(map[string]*time.Timer)
	rand.Seed(time.Now().UnixNano())

	return ws
}

func (ws *Webservice) ListenAndServe(routes func(*httprouter.Router)) {
	routes(ws.router)
	if ws.config.Insecure {
		fmt.Println("Listening on http://" + ws.config.ListenOn + "\n")
		log.Fatal(http.ListenAndServe(ws.config.ListenOn, ws.router))
	} else {
		if ws.config.UseCertmagic {
			log.Fatal(certmagic.HTTPS([]string{ws.config.CertmagicHost}, ws.router))
		} else {
			if ws.config.CertFile == "" || ws.config.KeyFile == "" {
				fmt.Println("Error: Please provide --tls-cert <file.crt> and --tls-key <file.key> or set --insecure.")
				fmt.Println("Exiting.")
				return
			}
			fmt.Println("Listening on https://" + ws.config.ListenOn + "\n")
			log.Fatal(http.ListenAndServeTLS(ws.config.ListenOn, ws.config.CertFile, ws.config.KeyFile, ws.router))
		}
	}
}

func (ws *Webservice) BasicAuth(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		// Get the Basic Authentication credentials
		user, password, hasAuth := r.BasicAuth()
		userExists, realPW := ws.GetPassword(user)
		if hasAuth && userExists && password == realPW {
			// Delegate request to the given handle
			switch user {
			case REQUESTVALIDATIONCODE:
				raw_data, _ := io.ReadAll(r.Body)
				valid := NewValidationCodeRequest()
				valid.getValidationCode = ws.GetValidationCode
				ws.AnswerRequest(w, raw_data, valid, user)
			case REQUESTCREATEACCOUNT:
				raw_data, _ := io.ReadAll(r.Body)
				account := NewCreateAccountRequest()
				account.validate = ws.Validate
				account.addUser = ws.AddUser
				ws.AnswerRequest(w, raw_data, account, user)
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

func (ws *Webservice) GetPassword(user string) (exists bool, password string) {
	if user == REQUESTVALIDATIONCODE || user == REQUESTCREATEACCOUNT {
		return true, ""
	}
	col := ws.db.GetCollection(ws.config.authKey)
	userExists, userdata := col.Get(user, PASSWORDFIELD)
	pw := ""
	if userExists {
		if len(userdata.Value2) >= 1 {
			pw = userdata.Value2[0]
		}
		// if password == "" then do something appropriate
	}

	return userExists, pw
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

func (ws *Webservice) GetAccount(account string) tiedb.Collection {
	key := tiedb.CollectionKey{ws.config.UserNamespace, account}
	if account == MAILSETTINGSDB {
		key = tiedb.CollectionKey{ws.config.AuthNamespace, MAILSETTINGSDB}
	}

	return ws.db.GetCollection(key)
}

func (ws *Webservice) AddUser(username, password string) {
	col := ws.db.GetCollection(ws.config.authKey)
	col.Add(username, PASSWORDFIELD, password)
}

func (ws *Webservice) DelUser(username, password string) bool {
	col := ws.db.GetCollection(ws.config.authKey)
	success, _ := col.Delete(username, PASSWORDFIELD, password)

	return success
}

func (ws *Webservice) UpdatePassword(username, password, newpassword string) bool {
	col := ws.db.GetCollection(ws.config.authKey)
	success, _ := col.Update(username, PASSWORDFIELD, password, newpassword)

	return success
}

func (ws *Webservice) SetMailSettings(mail MailSettings) {
	key := tiedb.CollectionKey{ws.config.AuthNamespace, MAILSETTINGSDB}
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
	reqName := ps.ByName("request")
	log.Println("Request from " + r.RemoteAddr + ": " + reqName)
	username, _, _ := r.BasicAuth() // Credentials already validated

	for _, x := range ws.requests {
		if reqName == x.GetId() {
			ws.AnswerRequest(w, raw_data, x, username)
			return
		}
	}

	log.Println("Unrecognized request from " + r.RemoteAddr + ": " + ps.ByName("type"))
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

func (ws *Webservice) AnswerRequest(w http.ResponseWriter, raw_data []byte, r RequestInterface, username string) {
	errRequest := json.Unmarshal(raw_data, r)
	if errRequest != nil {
		log.Println(errRequest)
		msg := ErrorToJsonString("Error unmarshalling request:", errRequest)
		fmt.Fprint(w, msg)
		return
	}

	reply, errReply := r.Reply(username, ws.GetAccount)
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
