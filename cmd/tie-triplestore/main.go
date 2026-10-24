package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/internal/daemon"
	"github.com/uidbz/tie/webservice"
)

// User is a single account entry in the triplestore config. Passwords are
// stored in plaintext, so the config file must be tightly permissioned.
type User struct {
	Username string
	Password string
	// Role is "read" (read-only) or "write" (full read+write). Empty defaults to
	// "write" so accounts predating roles keep full access.
	Role string
}

// configUsers maps the config file's [[Users]] entries onto the shared
// validating type, where duplicate names and empty passwords are refused.
func configUsers(us []User) []auth.UserConfig {
	out := make([]auth.UserConfig, 0, len(us))
	for _, u := range us {
		out = append(out, auth.UserConfig{Username: u.Username, Password: u.Password, Role: u.Role})
	}
	return out
}

// TripleStoreConfig is the full tie-triplestore configuration, loaded from TOML.
// It holds both the server settings and the list of accounts allowed to
// authenticate. Access management is done by editing this file, not through the
// wire protocol.
type TripleStoreConfig struct {
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
	// LoadMemoryLimitMB is a soft heap ceiling (GOMEMLIMIT semantics) applied
	// while collections load from disk. 0 = runtime default.
	LoadMemoryLimitMB int
	// MaxRequestBytesMB caps a single request body in megabytes. A body is read
	// whole before it is parsed, so an uncapped request can exhaust memory; a
	// Batch (restore, repair) legitimately carries a lot, so the built-in
	// default is generous. 0 selects it.
	MaxRequestBytesMB int
	// DisableLocking turns off the exclusive advisory lock on each collection's
	// .tie file. Locking (the default) stops a direct `tie -f` open or a second
	// server from writing the same file concurrently. Disable it only on a
	// filesystem where flock is unavailable or unreliable (some network mounts).
	DisableLocking bool
	Users          []User
	// AnonymousAccess is the role granted to a request with no valid credentials:
	// "none" (require auth for everything), "read", or "write". Empty defaults to
	// "none", preserving the always-authenticated behavior.
	AnonymousAccess string
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
// children via parent, media-type set-scoping (all hashes of a tie-type, so
// media queries can intersect a type with tag results), and version-of (listing
// a file's history in the "<Collection>_prev" collection). Restricting the
// reverse index to them keeps the bulk of file metadata (filename, size, ...)
// from doubling association memory; version-of costs nothing on collections that
// hold no version records (an empty reverse bucket). The reverse index is
// rebuilt from forward triples on startup, so changing this takes effect for
// existing data after one restart. member-of lists a media collection's
// members (album tracks across disc folders, playlist tracks) in one query —
// see client/media.go; it costs one reverse entry per member.
var defaultReverseRelations = []string{"tag", "path", "parent", "tie-type", "version-of", "member-of"}

func defaultConfig() TripleStoreConfig {
	return TripleStoreConfig{
		ListenOn: ":1161",
	}
}

func main() {
	flags := daemon.ParseFlags("tie-triplestore", "tie-triplestore.toml", `tie triplestore
-----------------------------
Configuration (server settings and user accounts) is read from a TOML file.
`)

	cfg := defaultConfig()
	if err := daemon.LoadConfig(flags.ConfigPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	cleanup := daemon.SetupLogging("tie-triplestore", cfg.LogFile, cfg.LogLevel)
	defer cleanup()

	authStore, err := auth.BuildStore(configUsers(cfg.Users), cfg.AnonymousAccess, auth.RoleNone)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error in config:", err)
		os.Exit(1)
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
		DbPath:                     cfg.DbPath,
		Auth:                       authStore,
		ReverseRelations:           reverseRelations,
		CollectionReverseRelations: overrides,
		MaxConcurrentRequests:      cfg.MaxConcurrentRequests,
		LoadMemoryLimitMB:          cfg.LoadMemoryLimitMB,
		MaxRequestBytes:            int64(cfg.MaxRequestBytesMB) * 1024 * 1024,
		DisableLocking:             cfg.DisableLocking,
	}

	ws := webservice.NewWebservice(config)
	api.RegisterAll(ws)

	routes := func(mux *http.ServeMux) {
		mux.HandleFunc("POST /{request}", ws.RequestHandler)
		// Identify the running build without speaking the protocol:
		// curl -s http://<host>:1161/-/version
		ws.HandleVersion("tie-triplestore")
	}

	if err := ws.ListenAndServe(routes); err != nil {
		slog.Error("triplestore stopped", "err", err)
		os.Exit(1)
	}
}
