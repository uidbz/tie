// serve project main.go
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/uidbz/conf"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/tielog"
	"github.com/uidbz/tie/version"
	"github.com/h2non/filetype"
	"github.com/minio/highwayhash"
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

var (
	key         []byte
	listenOn    string = ":1162"
	destination string = "/data"
	retention   *retentionIndex
	filecache   *blobCache
)

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
	BlobPath string
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
	tieKey       = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
	shardLevels  = 2    // directory nesting depth; uniform hashes cap dir count at 256^shardLevels
	dirWidth     = 2    // hex chars per level
	hashFunction = "hh" // highway hash
)

func InitKey() {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		slog.Error("cannot decode hex key", "err", err)
		return
	}
	key = k
}

func AddressOfFile(key []byte, file string) (string, error) { // function to compute address based on content
	fsocket, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer fsocket.Close()

	return AddressOf(key, fsocket)
}

func AddressOf(key []byte, input io.Reader) (string, error) { // function to compute address based on content
	hash, err := highwayhash.New(key)
	if err != nil {
		return "", err
	}

	_, err = io.Copy(hash, input)

	dest := hash.Sum(nil)

	return hex.EncodeToString(dest), err
}

func PathFromHash(dest, hash string) string {
	for i := 0; i < shardLevels*dirWidth; i += dirWidth {
		dest = filepath.Join(dest, hash[i:i+dirWidth])
	}

	return filepath.Join(dest, hash)
}

func MakeDestinationPath(hash string) (string, error) {
	dest := PathFromHash(destination, hash)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}

	return dest, nil
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	h := r.PathValue("hash")
	slog.Info("receiving file", "hash", h)
	var dest string
	defer func() {
		if r.Body != nil {
			r.Body.Close()
		}
	}()

	if h == "" {
		var errTmp error
		if dest, errTmp = os.MkdirTemp("", "tie-filehost"); errTmp != nil {
			fmt.Fprint(w, "Error creating temp dir on server:", errTmp)
			return
		}
		dest = filepath.Join(dest, "tempfile")
	} else {
		var errMake error
		if dest, errMake = MakeDestinationPath(h); errMake != nil {
			slog.Error("creating destination directory", "hash", h, "err", errMake)
			http.Error(w, "Error creating destination directory on server: "+errMake.Error(), http.StatusInternalServerError)
			return
		}
	}

	now := time.Now()
	expiresAt, errRet := parseRetention(r, now)
	if errRet != nil {
		http.Error(w, "Invalid Tie-Retention: "+errRet.Error(), http.StatusBadRequest)
		return
	}
	token := r.Header.Get("Tie-Owner")

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		if err := retention.recordUpload(h, expiresAt, token, now.Unix(), true); err != nil {
			slog.Error("recording retention", "err", err)
		}
		fmt.Fprint(w, h)
		return
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("opening destination file", "dest", dest, "err", err)
		http.Error(w, "Error opening destination file on server: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, errCopy := io.Copy(out, r.Body); errCopy != nil {
		slog.Error("copying upload body", "dest", dest, "err", errCopy)
		if err := os.Remove(dest); err != nil {
			slog.Error("deleting bad file", "dest", dest, "err", err)
		}
		http.Error(w, "Error writing upload body on server: "+errCopy.Error(), http.StatusInternalServerError)
		return
	}
	hashHex, _ := AddressOfFile(key, dest)
	if h != "" && hashHex != h {
		slog.Warn("checksum mismatch; deleting file", "expected", h, "calculated", hashHex, "dest", dest)
		if err := os.Remove(dest); err != nil {
			slog.Error("deleting bad file", "dest", dest, "err", err)
		}
	}
	blobExisted := false
	if h == "" {
		h = hashHex
		dest2, errMake := MakeDestinationPath(h)
		if errMake != nil {
			slog.Error("creating destination directory", "hash", h, "err", errMake)
			http.Error(w, "Error creating destination directory on server: "+errMake.Error(), http.StatusInternalServerError)
			os.RemoveAll(filepath.Dir(dest))
			return
		}
		if _, err := os.Stat(dest2); os.IsNotExist(err) {
			slog.Debug("moving file", "from", dest, "to", dest2)
			if errMove := os.Rename(dest, dest2); errMove != nil {
				fmt.Fprint(w, "Error saving file on server:", err)
				os.RemoveAll(filepath.Dir(dest))
				return
			}
		} else {
			os.RemoveAll(filepath.Dir(dest)) // file existed previously, delete tmp file
			blobExisted = true
		}
		dest = dest2
	}
	if err := retention.recordUpload(hashHex, expiresAt, token, now.Unix(), blobExisted); err != nil {
		slog.Error("recording retention", "err", err)
	}
	slog.Info("stored blob", "hash", hashHex)
	fmt.Fprint(w, hashHex)
}

func DownloadHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		slog.Warn("invalid hash requested", "hash", hash)
		fmt.Fprint(w, "Invalid hash:", hash)
		return
	}
	slog.Info("serving blob", "hash", hash)
	path := PathFromHash(destination, hash)
	if filecache != nil {
		if cached, err := filecache.get(hash); err == nil {
			path = cached
		} else {
			slog.Warn("cache miss, serving from store", "hash", hash, "err", err)
		}
	}
	// Blobs are stored under their hash with no extension, so http.ServeFile
	// falls back to net/http's content sniffer, whose signature table omits
	// common formats (e.g. FLAC), yielding application/octet-stream. Sniff the
	// magic number ourselves so the store serves a correct type; ServeFile
	// honours a Content-Type we set here rather than re-detecting.
	if ct := sniffContentType(path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, path)
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
	head := make([]byte, 262)
	n, _ := io.ReadFull(f, head)
	if n == 0 {
		return ""
	}
	if t, err := filetype.Get(head[:n]); err == nil && t != filetype.Unknown {
		return t.MIME.Value
	}
	return ""
}

// StatHandler reports whether a blob exists in the store, without transferring
// it. It answers HEAD /{hash}: 200 with a Content-Length when the blob is
// present, 404 when absent. Existence is checked against the primary store
// (BlobPath) directly — a blob is the durable content of record, so a blob that
// happens to be only in the cache still counts as existing, and a never-uploaded
// or reaped blob is a clean 404. The blob is never copied into the cache for a
// stat, and no body is read or written, so a full-store sweep costs one stat per
// hash.
func StatHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(PathFromHash(destination, hash))
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

func NamedDownloadHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	filename := r.PathValue("filename")
	if len(hash) != 64 {
		slog.Warn("invalid hash requested", "hash", hash)
		fmt.Fprint(w, "Invalid hash:", hash)
		return
	}
	slog.Info("serving blob", "hash", hash, "filename", filename)
	path := PathFromHash(destination, hash)
	if filecache != nil {
		if cached, err := filecache.get(hash); err == nil {
			path = cached
		} else {
			slog.Warn("cache miss, serving from store", "hash", hash, "err", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprint(w, "Error happened")
		return
	}
	reader, err2 := os.Open(path)
	if err2 != nil {
		fmt.Fprint(w, "Error happened")
		return
	}
	http.ServeContent(w, r, filename, info.ModTime(), reader)
}

// SetRetentionHandler is the explicit owner action to change a blob's expiry.
// It requires a matching Tie-Owner token when the blob is already owned; an
// unowned blob is claimed by the first token presented. Unlike upload, it may
// both shorten and extend the retention.
func SetRetentionHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	token := r.Header.Get("Tie-Owner")
	if !retention.authorized(hash, token) {
		http.Error(w, "Forbidden: owner token required", http.StatusForbidden)
		return
	}
	now := time.Now()
	expiresAt, err := parseRetention(r, now)
	if err != nil {
		http.Error(w, "Invalid Tie-Retention: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := retention.setRetention(hash, expiresAt, token, now.Unix()); err != nil {
		http.Error(w, "Error saving retention: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "ok")
}

// GetRetentionHandler reports a blob's current expiry. A blob absent from the
// index is permanent. The owner token hash is never returned.
func GetRetentionHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	e, ok := retention.get(hash)
	resp := struct {
		ExpiresAt int64 `json:"expires_at"`
		HasOwner  bool  `json:"has_owner"`
	}{}
	if ok {
		resp.ExpiresAt = e.ExpiresAt
		resp.HasOwner = e.OwnerTokenHash != ""
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

	if cfg.BlobPath == "" {
		slog.Error("please provide a BlobPath in the config file")
		os.Exit(1)
	}

	var reapInterval time.Duration
	if cfg.ReapInterval != "" {
		if reapInterval, err = time.ParseDuration(cfg.ReapInterval); err != nil {
			slog.Error("invalid ReapInterval", "err", err)
			os.Exit(1)
		}
	}

	users := make(map[string]auth.User, len(cfg.Users))
	for _, u := range cfg.Users {
		role, errRole := auth.ParseUserRole(u.Role)
		if errRole != nil {
			slog.Error("invalid user role in config", "user", u.Username, "err", errRole)
			os.Exit(1)
		}
		users[u.Username] = auth.User{Password: u.Password, Role: role}
	}
	anon, errAnon := auth.ParseAnonAccess(cfg.AnonymousAccess, auth.RoleWrite)
	if errAnon != nil {
		slog.Error("invalid AnonymousAccess in config", "err", errAnon)
		os.Exit(1)
	}
	authStore := auth.NewStore(users, anon)

	InitKey()
	router := http.NewServeMux()
	routes(router, authStore)

	destination = filepath.Clean(cfg.BlobPath)
	listenOn = cfg.ListenOn

	if cfg.CachePath != "" {
		maxBytes := int64(cfg.CacheSizeGB * 1024 * 1024 * 1024)
		if maxBytes <= 0 {
			maxBytes = 1 << 30 // 1 GiB default when CacheSizeGB is unset/zero
		}
		if filecache, err = newBlobCache(destination, filepath.Clean(cfg.CachePath), maxBytes); err != nil {
			slog.Error("initializing blob cache", "err", err)
			os.Exit(1)
		}
		slog.Info("blob cache enabled", "path", cfg.CachePath, "size_gb", cfg.CacheSizeGB)
	}

	if retention, err = loadRetentionIndex(destination); err != nil {
		slog.Error("loading retention index", "err", err)
		os.Exit(1)
	}
	startReaper(reapInterval)

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
