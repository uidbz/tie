package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"git.sr.ht/~uid/conf"
	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/tiedb"
	"git.sr.ht/~uid/tie/webservice"
)

var (
	__VERBOSE bool = true
	__DEBUG   bool = true

	db     *tiedb.TieTree
	dbPath string
)

// User is a single account entry in the daemon config. Passwords are stored in
// plaintext, so the config file must be tightly permissioned.
type User struct {
	Username string
	Password string
}

// DaemonConfig is the full tie-daemon configuration, loaded from TOML. It holds
// both the server settings and the list of accounts allowed to authenticate.
// Access management is done by editing this file, not through the wire protocol.
type DaemonConfig struct {
	ListenOn string
	Insecure bool
	CertFile string
	KeyFile  string
	DbPath   string
	// MaxConcurrentRequests caps how many requests run at once. 0 = unbounded.
	MaxConcurrentRequests int
	Users                 []User
}

func defaultConfig() DaemonConfig {
	return DaemonConfig{
		ListenOn: ":1161",
	}
}

func main() {
	var configPath = flag.String("config", "tie-daemon.toml", "Path to TOML config file.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `tie daemon
-----------------------------
Configuration (server settings and user accounts) is read from a TOML file.
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg := defaultConfig()
	if err := conf.ReadConfig(*configPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error reading config file:", err)
		os.Exit(1)
	}

	users := make(map[string]string, len(cfg.Users))
	for _, u := range cfg.Users {
		users[u.Username] = u.Password
	}

	config := webservice.WebserviceConfig{
		ListenOn:      cfg.ListenOn,
		Insecure:      cfg.Insecure,
		CertFile:      cfg.CertFile,
		KeyFile:       cfg.KeyFile,
		UserNamespace: "userdata",
		DbPath:        cfg.DbPath,
		Users:         users,
		// Only these relations are ever queried in reverse: tag lookups, path->UID,
		// UID children via parent, and media-type set-scoping (all hashes of a
		// tie-type, so media queries can intersect a type with tag results).
		// Restricting the reverse index to them keeps the bulk of file metadata
		// (filename, size, ...) from doubling association memory. The reverse index
		// is rebuilt from forward triples on startup, so adding a relation here
		// takes effect for existing data after one restart.
		ReverseRelations:      []string{"tag", "path", "parent", "tie-type"},
		MaxConcurrentRequests: cfg.MaxConcurrentRequests,
	}

	c := api.CollectionInfo{}

	requests := []webservice.RequestInterface{
		c.NewDummyRequest(),
		c.NewGetRequest(""),
		c.NewAddRequest("", "", ""),
		c.NewDeleteRequest("", "", ""),
		c.NewUpdateRequest(api.Update{}),
		api.NewBatchRequest(&api.Batch{}),
		c.NewAssociatedRequest(""),
		c.NewSyncRequest(),
		c.NewDumpRequest(),
	}

	ws := webservice.NewWebservice(config, requests)

	routes := func(mux *http.ServeMux) {
		mux.HandleFunc("POST /{request}", ws.BasicAuth(ws.RequestHandler))
	}

	ws.ListenAndServe(routes)
}
