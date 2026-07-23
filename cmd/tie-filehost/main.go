// serve project main.go
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"git.sr.ht/~uid/conf"
	"github.com/minio/highwayhash"
)

var (
	key         []byte
	listenOn    string = ":1162"
	destination string = "/data"
	retention   *retentionIndex
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
	// DbPath is the datastore root where content-addressed blobs are stored.
	DbPath string
	// ReapInterval is how often expired blobs are reaped, as a Go duration
	// string (e.g. "1h"). Empty or "0" disables the reaper.
	ReapInterval string
}

func defaultConfig() FilehostConfig {
	return FilehostConfig{
		ListenOn:     ":1162",
		DbPath:       "/data",
		ReapInterval: "1h",
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
		fmt.Printf("Cannot decode hex key: %v", err) // add error handling
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

func MakeDestinationPath(hash string) string {
	dest := PathFromHash(destination, hash)

	os.MkdirAll(filepath.Dir(dest), 0755) // TODO: Error handling

	return dest
}

func UploadHandler(w http.ResponseWriter, r *http.Request) {
	h := r.PathValue("hash")
	log.Println("Receiving file:", h)
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
		dest = MakeDestinationPath(h)
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
			log.Println("Error recording retention:", err)
		}
		fmt.Fprint(w, h)
		return
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
		if err := os.Remove(dest); err != nil {
			log.Println("Error deleting bad file:", dest, "Error message:", err.Error())
		}
	}
	defer out.Close()

	_, errCopy := io.Copy(out, r.Body)
	if errCopy != nil {
		log.Println(err)

	}
	hashHex, _ := AddressOfFile(key, dest)
	if h != "" && hashHex != h {
		log.Println("Checksum error! Expected:", h, "Calculated:", hashHex)
		log.Println("Deleting file:", dest)
		if err := os.Remove(dest); err != nil {
			log.Println("Error deleting bad file:", dest, "Error message:", err.Error())
		}
	}
	blobExisted := false
	if h == "" {
		h = hashHex
		dest2 := MakeDestinationPath(h)
		if _, err := os.Stat(dest2); os.IsNotExist(err) {
			fmt.Println("Moving ", dest, "to", dest2)
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
		log.Println("Error recording retention:", err)
	}
	log.Println("Calculated hash:", hashHex)
	fmt.Fprint(w, hashHex)
}

func DownloadHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 64 {
		fmt.Println("Invalid hash:", hash)
		fmt.Fprint(w, "Invalid hash:", hash)
		return
	}
	path := PathFromHash(destination, hash)
	fmt.Println("Client want:", hash)
	http.ServeFile(w, r, path)
}

func NamedDownloadHandler(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	filename := r.PathValue("filename")
	if len(hash) != 64 {
		fmt.Println("Invalid hash:", hash)
		fmt.Fprint(w, "Invalid hash:", hash)
		return
	}
	path := PathFromHash(destination, hash)
	fmt.Println("Client want:", hash)
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
		log.Println("Error encoding retention response:", err)
	}
}

func main() {
	var configPath = flag.String("config", "tie-filehost.toml", "Path to TOML config file.")

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

	cfg := defaultConfig()
	if err := conf.ReadConfig(*configPath, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error reading config file:", err)
		os.Exit(1)
	}

	if cfg.DbPath == "" {
		fmt.Fprintln(os.Stderr, "Please provide a DbPath in the config file")
		os.Exit(1)
	}

	var reapInterval time.Duration
	if cfg.ReapInterval != "" {
		var err error
		if reapInterval, err = time.ParseDuration(cfg.ReapInterval); err != nil {
			fmt.Fprintln(os.Stderr, "Invalid ReapInterval:", err)
			os.Exit(1)
		}
	}

	InitKey()
	router := http.NewServeMux()
	routes(router)

	destination = filepath.Clean(cfg.DbPath)
	listenOn = cfg.ListenOn

	var err error
	if retention, err = loadRetentionIndex(destination); err != nil {
		fmt.Fprintln(os.Stderr, "Error loading retention index:", err)
		os.Exit(1)
	}
	startReaper(reapInterval)

	if cfg.Insecure {
		fmt.Println("Listening on http://" + listenOn + "\n")
		log.Fatal(http.ListenAndServe(listenOn, router))
	} else {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			fmt.Fprintln(os.Stderr, "Error: set CertFile and KeyFile in the config file, or set Insecure = true.")
			os.Exit(1)
		}
		fmt.Println("Listening on https://" + listenOn + "\n")
		log.Fatal(http.ListenAndServeTLS(listenOn, cfg.CertFile, cfg.KeyFile, router))
	}
}
