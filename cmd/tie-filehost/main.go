// serve project main.go
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/caddyserver/certmagic"
	"github.com/julienschmidt/httprouter"
	"github.com/minio/highwayhash"
)

var (
	key          []byte
	listenOn     string = ":1162"
	destination  string = "/data"
	thumbnailDir string = ""
)

const (
	tieKey       = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
	lvlDeep      = 3
	dirWidth     = 2
	max          = lvlDeep * dirWidth
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

func UploadHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	h := p.ByName("hash")
	jsonOut := p.ByName("json")
	log.Println("Receiving file:", h)
	var dest string = destination

	max := lvlDeep * dirWidth
	for i := 0; i <= max; i = i + dirWidth {
		dest = filepath.Join(dest, h[i:i+dirWidth])
	}
	os.MkdirAll(dest, 0755)
	dest = filepath.Join(dest, h)

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		jsonData, _, _ := GetMetadata(dest, h)
		switch jsonOut {
		case "json":
			fmt.Fprint(w, string(jsonData))
		case "json-force-generate-thumbnails":
			_, mediatype, _ := GetMetadata(dest, h)
			GenerateThumbnail(thumbnailDir, dest, h, mediatype)
			fmt.Fprint(w, string(jsonData))
		default:
			fmt.Fprint(w, h)
		}
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

	f, _, errFormFile := r.FormFile("file")
	if errFormFile != nil {
		fmt.Fprint(w, h)
		log.Println(errFormFile.Error())
		return

	}
	_, errCopy := io.Copy(out, f)
	if errCopy != nil {
		log.Println(err)

	}
	hashHex, _ := AddressOfFile(key, dest)
	if hashHex != h {
		log.Println("Checksum error! Expected:", h, "Calculated:", hashHex)
		log.Println("Deleting file:", dest)
		if err := os.Remove(dest); err != nil {
			log.Println("Error deleting bad file:", dest, "Error message:", err.Error())
		}
	}
	log.Println("Calculated hash:", hashHex)
	jsonData, mediatype, _ := GetMetadata(dest, hashHex)
	GenerateThumbnail(thumbnailDir, dest, hashHex, mediatype)
	if jsonOut == "json" {
		fmt.Fprint(w, string(jsonData))
	} else {
		fmt.Fprint(w, hashHex)
	}
}

func DownloadHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	hash := p.ByName("hash")
	var path string = destination
	if len(hash) != 64 {
		fmt.Println("Invalid hash:", hash)
		fmt.Fprint(w, "Invalid hash:", hash)
		return
	}
	max := lvlDeep * dirWidth
	for i := 0; i <= max; i = i + dirWidth {
		path = filepath.Join(path, hash[i:i+dirWidth])
	}
	path = filepath.Join(path, hash)
	fmt.Println("Client want:", hash)
	http.ServeFile(w, r, path)
}

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var path = flag.String("path", "/data", "Path to store data.")
	var addr = flag.String("listen", ":1162", "Listen on particular address/port (ignored if using certmagic).")
	var thumbs = flag.String("thumbs", "", "Path to store thumbnails of uploaded media files.")
	var host = flag.String("host", "", "Hostname for certmagic")
	var useCertmagic = flag.Bool("certmagic", false, "Use Let's encrypt for TLS certificate")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `Content-addressed file server
-----------------------------
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	InitKey()
	router := httprouter.New()
	routes(router)
	if *path == "" {
		fmt.Println("Please provide a data-path")
		return
	}
	destination = filepath.Clean(*path)
	listenOn = *addr
	if *thumbs != "" {
		thumbnailDir = filepath.Clean(*thumbs)
	}

	if *insecure {
		fmt.Println("Listening on http://" + listenOn + "\n")
		log.Fatal(http.ListenAndServe(listenOn, router))
	} else {
		if *useCertmagic {
			log.Fatal(certmagic.HTTPS([]string{*host}, router))
		} else {
			if *certFile == "" || *keyFile == "" {
				fmt.Println("Error: Please provide --tls-cert <file.crt> and --tls-key <file.key> or set --insecure.")
				fmt.Println("Exiting.")
				return
			}
			fmt.Println("Listening on https://" + listenOn + "\n")
			log.Fatal(http.ListenAndServeTLS(listenOn, *certFile, *keyFile, router))
		}
	}
}
