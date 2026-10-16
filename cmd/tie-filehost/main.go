// serve project main.go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/h2non/filetype"
	"github.com/uidbz/conf"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/tielog"
	"github.com/uidbz/tie/version"
)

// User is a single account entry in the filehost config. Passwords are stored
// in plaintext, so the config file must be tightly permissioned.
type User struct {
	Username string
	Password string
	// Role is "read" (download only) or "write" (upload + download). Empty
	// defaults to "write".
	Role string
}

// configUsers maps the config file's [[Users]] entries onto the shared
// validating type, where duplicate names and empty passwords are refused.
func configUsers(us []User) []auth.ConfigUser {
	out := make([]auth.ConfigUser, 0, len(us))
	for _, u := range us {
		out = append(out, auth.ConfigUser{Username: u.Username, Password: u.Password, Role: u.Role})
	}
	return out
}

// FilehostConfig is the full tie-filehost configuration, loaded from TOML.
// TLS is expected to be terminated by a reverse proxy in front of the filehost
// (run Insecure on localhost); alternatively point CertFile/KeyFile at a cert
// pair to serve HTTPS directly.
type FilehostConfig struct {
	ListenOn string
	Insecure bool
	CertFile string
	KeyFile  string
	// BlobPath is the root directory where content-addressed blobs are stored.
	// Deprecated in favor of BlobPaths; when BlobPaths is empty a single
	// "default" store is synthesized from BlobPath (permanent retention).
	BlobPath string
	// BlobPaths configures one or more named physical stores. Uploads route to a
	// store by the Tie-Store header (empty targets the "default" store, or the
	// first listed); reads search all stores by hash. Each store keeps its own
	// retention index and DefaultRetention, so dedup is per-store.
	BlobPaths []BlobPathConfig
	// ReapInterval is how often expired blobs are reaped, as a Go duration
	// string (e.g. "1h"). Empty or "0" disables the reaper.
	ReapInterval string
	// LogFile is the path structured JSON logs are appended to. Empty logs only
	// to stderr (pretty).
	LogFile string
	// LogLevel is the minimum level emitted: debug, info, warn, error. Empty
	// defaults to info.
	LogLevel string
	// CachePath is an optional directory on faster storage (e.g. an SSD or
	// tmpfs) where blobs are copied on first access and served from thereafter.
	// Leave empty to disable; blobs are served directly from BlobPath.
	CachePath string
	// CacheSizeGB is the LRU eviction budget for CachePath in gibibytes.
	// Defaults to 1. Ignored when CachePath is empty.
	CacheSizeGB float64
	// Users are the accounts allowed to authenticate. Empty leaves the filehost
	// open (see AnonymousAccess).
	Users []User
	// AnonymousAccess is the role granted to a request with no valid credentials:
	// "write" (fully open — the default, preserving unauthenticated uploads),
	// "read" (anonymous downloads, uploads require a write user), or "none"
	// (every request requires auth).
	AnonymousAccess string
}

func defaultConfig() FilehostConfig {
	return FilehostConfig{
		ListenOn:        ":1162",
		BlobPath:        "/data",
		ReapInterval:    "1h",
		CacheSizeGB:     1,
		AnonymousAccess: "write",
	}
}

// parseRetention reads the Tie-Retention header into an absolute unix expiry.
// Absent or "infinite" means permanent (0). Otherwise the value is a Go
// duration (e.g. "72h") added to now.
func parseRetention(r *http.Request, now time.Time) (int64, error) {
	v := r.Header.Get("Tie-Retention")
	if v == "" || v == "infinite" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	return now.Add(d).Unix(), nil
}

const (
	shardLevels  = 2    // directory nesting depth; uniform hashes cap dir count at 256^shardLevels
	dirWidth     = 2    // hex chars per level
	hashFunction = "hh" // highway hash
)

// PathFromHash maps a content address to its sharded location under dest. It
// rejects anything that is not a well-formed hash, so a request-supplied
// value (ServeMux hands wildcards back unescaped — %2F becomes /) can never
// traverse out of the store root.
func PathFromHash(dest, hash string) (string, error) {
	if !metadata.IsHexHash(hash) {
		return "", fmt.Errorf("malformed content address %q", hash)
	}
	for i := 0; i < shardLevels*dirWidth; i += dirWidth {
		dest = filepath.Join(dest, hash[i:i+dirWidth])
	}

	return filepath.Join(dest, hash), nil
}

func (s *server) uploadHandler(w http.ResponseWriter, r *http.Request, h string) {
	defer func() {
		if r.Body != nil {
			r.Body.Close()
		}
	}()

	st := s.storeByName(r.Header.Get("Tie-Store"))
	if st == nil {
		http.Error(w, "Unknown store: "+r.Header.Get("Tie-Store"), http.StatusBadRequest)
		return
	}

	now := time.Now()
	expiresAt, errRet := st.uploadExpiry(r, now)
	if errRet != nil {
		http.Error(w, "Invalid Tie-Retention: "+errRet.Error(), http.StatusBadRequest)
		return
	}
	token := r.Header.Get("Tie-Owner")

	hashHex, existed, isDir, err := putBlob(st, r.Body, h)
	switch {
	case errors.Is(err, errChecksumMismatch):
		slog.Warn("checksum mismatch; nothing stored", "expected", h)
		http.Error(w, "Checksum mismatch", http.StatusUnprocessableEntity)
		return
	case err != nil:
		slog.Error("storing blob", "store", st.name, "err", err)
		http.Error(w, "Error saving file on server: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := st.retention.recordUpload(hashHex, expiresAt, token, now.Unix(), existed); err != nil {
		slog.Error("recording retention", "err", err)
	}
	// Index directory manifests as they land, so the reaper's mark phase walks
	// this set instead of opening every blob in the store (§5.2 item 9).
	if isDir {
		st.dirs.add(hashHex)
	}
	slog.Info("stored blob", "hash", hashHex, "store", st.name)
	fmt.Fprint(w, hashHex)
}

func (s *server) downloadHandler(w http.ResponseWriter, r *http.Request, hash string, st *blobStore) {
	slog.Info("serving blob", "hash", hash)
	// The name passed to ServeContent is the blob's file name, which is what
	// http.ServeFile would have used.
	s.serveBlob(w, r, hash, hash, st)
}

// serveBlob answers a GET for hash. With a read cache configured, a cached
// copy is served through its handle — so an eviction mid-request cannot turn a
// download into a 404 — and a cold blob is answered straight from the store
// while the cache fills behind the response. Copying the whole blob before the
// first byte used to put seconds to minutes between a client's request and the
// first byte of a multi-GB object (§4.16).
func (s *server) serveBlob(w http.ResponseWriter, r *http.Request, name, hash string, st *blobStore) {
	path, err := PathFromHash(st.path, hash)
	if err != nil { // unreachable: withHash validated the hash
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.cache != nil {
		if h, ok := s.cache.peek(hash); ok {
			defer h.Close()
			if ct := sniffReader(h.Section()); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			http.ServeContent(w, r, name, info.ModTime(), h.Section())
			return
		}
		defer s.cache.fill(hash, path)
	}
	// Blobs are stored under their hash with no extension, so http.ServeContent
	// falls back to net/http's content sniffer, whose signature table omits
	// common formats (e.g. FLAC), yielding application/octet-stream. Sniff the
	// magic number ourselves so the store serves a correct type; ServeContent
	// honours a Content-Type we set here rather than re-detecting.
	if ct := sniffContentType(path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// sniffContentType returns a MIME type detected from the file's leading bytes
// via magic-number matching, or "" when the type is unrecognised or the file
// cannot be read. filetype inspects at most the first 262 bytes.
func sniffContentType(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return sniffReader(f)
}

// sniffReader detects the type of whatever r reads, so the same magic-number
// match works on a store file and on a cache handle.
func sniffReader(r io.Reader) string {
	head := make([]byte, 262)
	n, _ := io.ReadFull(r, head)
	return sniffHead(head[:n])
}

func sniffHead(head []byte) string {
	if len(head) == 0 {
		return ""
	}
	if t, err := filetype.Get(head); err == nil && t != filetype.Unknown {
		return t.MIME.Value
	}
	return ""
}

// versionHandler reports the running build as JSON: {"server":"tie-filehost",
// "build":{version, commit, date, dirty, goVersion}} — the same shape as the
// triplestore's Version reply, so `tie version` can print both alike.
func (s *server) versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"server": "tie-filehost",
		"build":  version.Get(),
	})
}

// statHandler reports whether a blob exists in the store, without transferring
// it. It answers HEAD /{hash}: 200 with a Content-Length when the blob is
// present, 404 when absent. Existence is checked against the primary store
// (BlobPath) directly — a blob is the durable content of record, so a blob that
// happens to be only in the cache still counts as existing, and a never-uploaded
// or reaped blob is a clean 404. The blob is never copied into the cache for a
// stat, and no body is read or written, so a full-store sweep costs one stat per
// hash.
func (s *server) statHandler(w http.ResponseWriter, r *http.Request, hash string, st *blobStore) {
	p, err := PathFromHash(st.path, hash)
	if err != nil { // unreachable: withHash validated the hash
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		slog.Error("stat blob", "hash", hash, "err", err)
		http.Error(w, "Error stat blob", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
}

func (s *server) namedDownloadHandler(w http.ResponseWriter, r *http.Request, hash string, st *blobStore) {
	filename := r.PathValue("filename")
	slog.Info("serving blob", "hash", hash, "filename", filename)
	s.serveBlob(w, r, filename, hash, st)
}

// setRetentionHandler is the explicit owner action to change a blob's expiry.
// It requires a matching Tie-Owner token when the blob is already owned; an
// unowned blob is claimed by the first token presented. Unlike upload, it may
// both shorten and extend the retention.
func (s *server) setRetentionHandler(w http.ResponseWriter, r *http.Request, hash string, st *blobStore) {
	token := r.Header.Get("Tie-Owner")
	if !st.retention.authorized(hash, token) {
		http.Error(w, "Forbidden: owner token required", http.StatusForbidden)
		return
	}
	now := time.Now()
	expiresAt, err := parseRetention(r, now)
	if err != nil {
		http.Error(w, "Invalid Tie-Retention: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := st.retention.setRetention(hash, expiresAt, token, now.Unix()); err != nil {
		http.Error(w, "Error saving retention: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "ok")
}

// getRetentionHandler reports a blob's current expiry. A blob absent from the
// index is permanent. The owner token hash is never returned.
func (s *server) getRetentionHandler(w http.ResponseWriter, r *http.Request, hash string) {
	resp := struct {
		ExpiresAt int64 `json:"expires_at"`
		HasOwner  bool  `json:"has_owner"`
	}{}
	if st, found := s.findBlob(hash); found {
		if e, ok := st.retention.get(hash); ok {
			resp.ExpiresAt = e.ExpiresAt
			resp.HasOwner = e.OwnerTokenHash != ""
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encoding retention response", "err", err)
	}
}

func main() {
	var configPath = flag.String("config", "tie-filehost.toml", "Path to TOML config file.")
	var showVersion = flag.Bool("version", false, "Print version and exit.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `Content-addressed file server
-----------------------------
Configuration (data path, listen address, TLS) is read from a TOML file.
TLS is normally terminated by a reverse proxy: run Insecure on localhost, or
set CertFile/KeyFile to serve HTTPS directly.
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("tie-filehost", version.String())
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
	tielog.LogVersion("tie-filehost")

	if cfg.BlobPath == "" && len(cfg.BlobPaths) == 0 {
		slog.Error("please provide a BlobPath or BlobPaths in the config file")
		os.Exit(1)
	}

	var reapInterval time.Duration
	if cfg.ReapInterval != "" {
		if reapInterval, err = time.ParseDuration(cfg.ReapInterval); err != nil {
			slog.Error("invalid ReapInterval", "err", err)
			os.Exit(1)
		}
	}

	anon, errAnon := auth.ParseAnonAccess(cfg.AnonymousAccess, auth.RoleWrite)
	if errAnon != nil {
		slog.Error("invalid AnonymousAccess in config", "err", errAnon)
		os.Exit(1)
	}
	authStore, errAuth := auth.BuildStore(configUsers(cfg.Users), anon)
	if errAuth != nil {
		slog.Error("invalid users in config", "err", errAuth)
		os.Exit(1)
	}

	srv := &server{}
	if err := srv.initStores(cfg); err != nil {
		slog.Error("configuring blob stores", "err", err)
		os.Exit(1)
	}
	for _, st := range srv.stores {
		slog.Info("blob store", "name", st.name, "path", st.path, "defaultRetention", st.defaultRetention)
	}

	if cfg.CachePath != "" {
		maxBytes := int64(cfg.CacheSizeGB * 1024 * 1024 * 1024)
		if maxBytes <= 0 {
			maxBytes = 1 << 30 // 1 GiB default when CacheSizeGB is unset/zero
		}
		if srv.cache, err = newBlobCache(filepath.Clean(cfg.CachePath), maxBytes); err != nil {
			slog.Error("initializing blob cache", "err", err)
			os.Exit(1)
		}
		slog.Info("blob cache enabled", "path", cfg.CachePath, "size_gb", cfg.CacheSizeGB)
	}

	router := http.NewServeMux()
	srv.routes(router, authStore)

	startReaper(reapInterval, srv)

	listenOn := cfg.ListenOn
	if cfg.Insecure {
		slog.Info("listening", "addr", "http://"+listenOn)
		if err := http.ListenAndServe(listenOn, router); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	} else {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			slog.Error("set CertFile and KeyFile in the config file, or set Insecure = true")
			os.Exit(1)
		}
		slog.Info("listening", "addr", "https://"+listenOn)
		if err := http.ListenAndServeTLS(listenOn, cfg.CertFile, cfg.KeyFile, router); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}
}
