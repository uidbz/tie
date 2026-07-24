package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"git.sr.ht/~uid/conf"
	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/tiedb"
	"git.sr.ht/~uid/tie/tielog"
	"git.sr.ht/~uid/tie/version"
	"git.sr.ht/~uid/tie/webservice"
)

var (
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
	// LogFile is the path structured JSON logs are appended to. Empty logs only
	// to stderr (pretty).
	LogFile string
	// LogLevel is the minimum level emitted: debug, info, warn, error. Empty
	// defaults to info.
	LogLevel string
	// MaxConcurrentRequests caps how many requests run at once. 0 = unbounded.
	MaxConcurrentRequests int
	Users                 []User
	// ReverseRelations is the default set of relations (value1) indexed in
	// reverse for every collection. Empty falls back to defaultReverseRelations
	// below. Restricting it cuts association memory on metadata-heavy stores.
	ReverseRelations []string
	// Collections holds per-collection overrides of ReverseRelations. A restart
	// is required for a change to take effect (the reverse index is rebuilt from
	// forward triples at load time).
	Collections []CollectionConfig
}

// CollectionConfig overrides the reverse-relation set for a single collection,
// keyed by its namespace and id.
type CollectionConfig struct {
	Namespace        string
	Collection       string
	ReverseRelations []string
}

// defaultReverseRelations is the built-in default when the config sets none.
// Only these relations are ever queried in reverse: tag lookups, path->UID, UID
// children via parent, and media-type set-scoping (all hashes of a tie-type, so
// media queries can intersect a type with tag results). Restricting the reverse
// index to them keeps the bulk of file metadata (filename, size, ...) from
// doubling association memory. The reverse index is rebuilt from forward triples
// on startup, so changing this takes effect for existing data after one restart.
var defaultReverseRelations = []string{"tag", "path", "parent", "tie-type"}

func defaultConfig() DaemonConfig {
	return DaemonConfig{
		ListenOn: ":1161",
	}
}

func main() {
	var configPath = flag.String("config", "tie-daemon.toml", "Path to TOML config file.")
	var showVersion = flag.Bool("version", false, "Print version and exit.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `tie daemon
-----------------------------
Configuration (server settings and user accounts) is read from a TOML file.
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("tie-daemon", version.String())
		return
	}

	cfg := defaultConfig()
	if err := conf.ReadConfig(*configPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error reading config file:", err)
		os.Exit(1)
	}

	cleanup, err := tielog.Setup(tielog.Config{File: cfg.LogFile, Level: cfg.LogLevel})
	if err != nil {
		slog.Error("could not open log file, logging to stderr only", "file", cfg.LogFile, "err", err)
	}
	defer cleanup()

	users := make(map[string]string, len(cfg.Users))
	for _, u := range cfg.Users {
		users[u.Username] = u.Password
	}

	reverseRelations := cfg.ReverseRelations
	if len(reverseRelations) == 0 {
		reverseRelations = defaultReverseRelations
	}

	overrides := make([]webservice.CollectionReverseRelations, 0, len(cfg.Collections))
	for _, c := range cfg.Collections {
		overrides = append(overrides, webservice.CollectionReverseRelations{
			Namespace:  c.Namespace,
			Collection: c.Collection,
			Relations:  c.ReverseRelations,
		})
	}

	config := webservice.WebserviceConfig{
		ListenOn:                   cfg.ListenOn,
		Insecure:                   cfg.Insecure,
		CertFile:                   cfg.CertFile,
		KeyFile:                    cfg.KeyFile,
		UserNamespace:              "userdata",
		DbPath:                     cfg.DbPath,
		Users:                      users,
		ReverseRelations:           reverseRelations,
		CollectionReverseRelations: overrides,
		MaxConcurrentRequests:      cfg.MaxConcurrentRequests,
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
