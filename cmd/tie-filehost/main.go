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
	"github.com/minio/highwayhash"
)

var (
	key         []byte
	listenOn    string = ":1162"
	destination string = "/data"
)

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
	jsonOut := r.PathValue("json")
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

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		jsonData, _, _ := GetMetadata(dest, h)
		switch jsonOut {
		case "json":
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
		}
		dest = dest2
	}
	log.Println("Calculated hash:", hashHex)
	jsonData, _, _ := GetMetadata(dest, hashHex)
	if jsonOut == "json" {
		fmt.Fprint(w, string(jsonData))
	} else {
		fmt.Fprint(w, hashHex)
	}
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

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var path = flag.String("path", "/data", "Path to store data.")
	var addr = flag.String("listen", ":1162", "Listen on particular address/port (ignored if using certmagic).")
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
	router := http.NewServeMux()
	routes(router)
	if *path == "" {
		fmt.Println("Please provide a data-path")
		return
	}
	destination = filepath.Clean(*path)
	listenOn = *addr

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
