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

	"git.sr.ht/~uid/tie/metadata"
	"github.com/minio/highwayhash"
)

const (
	tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
)

type PutConfig struct {
	PathToWorkdir           bool
	JsonOutput              bool
	ForceGenerateThumbnails bool
	currentKey              []byte
}

type Info struct {
	Hash      string
	MediaType string
}

type Status struct {
	LastItem      StatusItem
	UploadedItems []StatusItem
	ErrorMsg      string
}

type StatusItem struct {
	Hash      string
	MediaType string
	Filename  string
	ErrorMsg  string
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

func (pc *PutConfig) UploadFile(url string, path string) StatusItem {
	f, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error()}
	}
	defer f.Close()

	return pc.UploadMultipart(url, f, path)
}

func (pc *PutConfig) UploadMultipart(url string, f io.Reader, path string) StatusItem {
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
		if partWriter == nil || bufferedFileReader == nil {
			return
		}
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
		return StatusItem{
			Filename: path,
			ErrorMsg: "Upload error:" + err.Error(),
		}
	}
	req.Header.Add("Content-Type", formWriter.FormDataContentType())

	// This operation will block until both the formWriter
	// and bodyWriter have been closed by the goroutine,
	// or in the event of a HTTP error.
	resp, err := http.DefaultClient.Do(req)

	if writeErr != nil {
		return StatusItem{
			Filename: path,
			ErrorMsg: "Upload error:" + writeErr.Error(),
		}
	}

	if err != nil {
		return StatusItem{
			Filename: path,
			ErrorMsg: "Upload error:" + err.Error(),
		}
	}

	body, errResp := io.ReadAll(resp.Body)
	if errResp != nil {
		return StatusItem{ErrorMsg: errResp.Error()}
	}
	var hash string
	if pc.JsonOutput {
		info := metadata.Info{}
		err := json.Unmarshal(body, &info)
		if err != nil {
			return StatusItem{
				Filename: path,
				ErrorMsg: "Upload error (json unmarshall):" + err.Error(),
			}
		}
		hash = info.Hash
		return StatusItem{
			Hash:      hash,
			Filename:  path,
			MediaType: info.MediaType,
		}
	} else {
		return StatusItem{
			Hash:     string(body),
			Filename: path,
		}
	}
}

func Upload(url string, file string, config PutConfig) *Status {
	if len(url) < 5 {
		status := Status{
			ErrorMsg: "Url to short",
		}
		return &status
	}
	if url[len(url)-1] == '/' {
		url += "upload/"
	} else {
		url += "/upload/"
	}
	// Change workdir temporarily to input dir, to get relative paths in dir enumeration
	if config.PathToWorkdir {
		wd, _ := os.Getwd()
		defer os.Chdir(wd)

		var dir string
		fi, err := os.Stat(file)
		if fi.IsDir() {
			dir = file
			file = "."
		} else {
			dir = filepath.Dir(file)
			file = filepath.Base(file)
		}
		err = os.Chdir(dir)
		if err != nil {
			return &Status{ErrorMsg: "Error changing dir to:" + dir + " : " + err.Error()}
		}
	}

	status := Status{}
	upload(url, file, config, &status)

	return &status
}

func upload(url string, file string, config PutConfig, status *Status) {
	fi, errStat := os.Lstat(file)
	if errStat != nil {
		status.ErrorMsg += "Error stat file: " + file + errStat.Error()
		return
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
			upload(url, abs, config, status)
			hashes += status.LastItem.Hash + "\t" + status.LastItem.Filename + "\n"
		}
		localhash, _ := config.AddressOf(strings.NewReader(hashes))
		uploadStatus := config.UploadMultipart(url+localhash, strings.NewReader(hashes), file)
		config.Validate(localhash, uploadStatus, status)
	} else {
		localhash, _ := config.AddressOfFile(file)
		uploadStatus := config.UploadFile(url+localhash, file)
		config.Validate(localhash, uploadStatus, status)
	}
}

func (pc *PutConfig) Validate(localhash string, uploadStatus StatusItem, status *Status) {
	if localhash != uploadStatus.Hash {
		uploadStatus.ErrorMsg += "Upload checksum failed"
	}
	status.LastItem = uploadStatus

	if status.LastItem.ErrorMsg != "" {
		status.ErrorMsg += status.LastItem.ErrorMsg + "\n"
	}

	status.UploadedItems = append(status.UploadedItems, status.LastItem)
}
