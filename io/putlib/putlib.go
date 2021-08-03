// putlib project main.go
package putlib

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/minio/highwayhash"
)

const (
	tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
)

type PutConfig struct {
	JsonOutput              bool
	ForceGenerateThumbnails bool
	currentKey              []byte
}

type Info struct {
	Hash      string
	MediaType string
}

func InitKey() []byte {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		fmt.Printf("Cannot decode hex key: %v", err) // add error handling
		return nil
	}
	return k
}

func (pc *PutConfig) AddressOfFile(file string) (string, error) { // function to compute address based on content
	fsocket, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer fsocket.Close()

	return pc.AddressOf(fsocket)
}

func (pc *PutConfig) AddressOf(input io.Reader) (string, error) { // function to compute address based on content
	if len(pc.currentKey) != 32 {
		pc.currentKey = InitKey()
	}

	hash, err := highwayhash.New(pc.currentKey)
	if err != nil {
		return "", err
	}

	_, err = io.Copy(hash, input)

	dest := hash.Sum(nil)

	return hex.EncodeToString(dest), err
}

func (pc *PutConfig) UploadFile(url string, path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return "", err
	}
	defer f.Close()

	return pc.UploadMultipart(url, f, path)
}

func (pc *PutConfig) UploadMultipart(url string, f io.Reader, path string) (string, error) {
	// Reduce number of syscalls when reading from disk.
	bufferedFileReader := bufio.NewReader(f)

	// Create a pipe for writing from the file and reading to
	// the request concurrently.
	bodyReader, bodyWriter := io.Pipe()
	formWriter := multipart.NewWriter(bodyWriter)

	// Store the first write error in writeErr.
	var (
		writeErr error
		errOnce  sync.Once
	)
	setErr := func(err error) {
		if err != nil {
			errOnce.Do(func() { writeErr = err })
		}
	}
	go func() {
		partWriter, err := formWriter.CreateFormFile("file", path)
		setErr(err)
		_, err = io.Copy(partWriter, bufferedFileReader)
		setErr(err)
		setErr(formWriter.Close())
		setErr(bodyWriter.Close())
	}()

	if pc.JsonOutput {
		url += "/json"
	}
	if pc.ForceGenerateThumbnails {
		url += "-force-generate-thumbnails"
	}
	req, err := http.NewRequest(http.MethodPut, url, bodyReader)
	if err != nil {
		return "", err
	}
	req.Header.Add("Content-Type", formWriter.FormDataContentType())

	// This operation will block until both the formWriter
	// and bodyWriter have been closed by the goroutine,
	// or in the event of a HTTP error.
	resp, err := http.DefaultClient.Do(req)

	if writeErr != nil {
		return "", writeErr
	}

	if err != nil {
		return "", err
	}

	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func (pc *PutConfig) Upload(url string, file string) (string, string) {
	fi, errStat := os.Lstat(file)
	if errStat != nil {
		fmt.Println("Error stat file:", file, errStat)
		return "", ""
	}
	if len(url) < 5 {
		return "", ""
	}
	if url[len(url)-1] != '/' {
		url += "/"
	}

	if fi.IsDir() {
		entries, err := os.ReadDir(file)
		if err != nil {
			fmt.Println("Error reading directory", file, "Error:", err.Error())
		}
		var hashes string = "dir\n---\n"
		for _, x := range entries {
			abs := filepath.Join(file, x.Name())
			abs = strings.ReplaceAll(abs, "\\", "/") // Replace Windows folder separator with slash
			h, f := pc.Upload(url, abs)
			hashes += h + "\t" + f + "\n"
		}
		h, _ := pc.AddressOf(strings.NewReader(hashes))
		output, err := pc.UploadMultipart(url+h, strings.NewReader(hashes), file)
		if err != nil {
			fmt.Println("Upload directory error:", err)
		}
		return pc.ValidateAndPrint(h, file, output)
	} else {
		h, _ := pc.AddressOfFile(file)
		output, err := pc.UploadFile(url+h, file)
		if err != nil {
			fmt.Println("Upload error:", err)
		}
		return pc.ValidateAndPrint(h, file, output)
	}
}

func (pc *PutConfig) ValidateAndPrint(h string, file string, output string) (string, string) {
	if pc.JsonOutput {
		h2 := Info{}
		if err := json.Unmarshal([]byte(output), &h2); err != nil {
			fmt.Println("Unmashal error:", err.Error())
		}
		if h != h2.Hash {
			fmt.Println("Upload checksum failed")
		}
		// fmt.Println(output)
		return output, file
	} else {
		if h != string(output) {
			fmt.Println("Upload checksum failed")
		}
		fmt.Println(string(output), "\t", file)
		return h, file
	}

}
