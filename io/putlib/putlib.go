// putlib project main.go
package putlib

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/h2non/filetype"

	"git.sr.ht/~uid/tie/metadata"
	"github.com/minio/highwayhash"
)

const (
	tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
)

type PutConfig struct {
	PathToWorkdir bool
	JsonOutput    bool
	// Client is the HTTP client used for uploads. When nil, http.DefaultClient
	// is used. Set it to control TLS behavior (e.g. InsecureSkipVerify).
	Client     *http.Client
	currentKey []byte
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
	Size      int
	Head      []byte // first bytes of the content, for recomputable media-type detection
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
	defer f.Close()

	if err != nil {
		return StatusItem{ErrorMsg: err.Error()}
	}

	fi, err := os.Stat(path)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error()}
	}

	return pc.UploadMultipart(url, f, int(fi.Size()), path)
}

func (pc *PutConfig) UploadMultipart(url string, f io.Reader, length int, path string) StatusItem {
	var (
		writeErr error // Store the first write error in writeErr.
		errOnce  sync.Once
	)
	check := func(err error) {
		if err != nil {
			errOnce.Do(func() { writeErr = err })
		}
	}

	bufferedFileReader := bufio.NewReader(f)

	if pc.JsonOutput {
		url += "/json"
	}
	req, err := http.NewRequest(http.MethodPut, url, bufferedFileReader)
	check(err)

	contentType := "application/octet-stream"
	b, _ := bufferedFileReader.Peek(metadata.MagicNumber) // returns available bytes even for short files
	head := append([]byte(nil), b...)
	if t, err := filetype.Get(b); err == nil {
		contentType = t.MIME.Value
	}
	req.Header.Add("Content-Type", contentType)
	req.Header.Add("Content-Length", strconv.Itoa(length))

	httpClient := pc.Client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()

	check(err)

	body := make([]byte, 0)
	if resp == nil {
		check(errors.New("Empty response received"))
	} else {
		body, err = io.ReadAll(resp.Body)
		check(err)
	}

	var errorMsg string
	if writeErr != nil {
		errorMsg = writeErr.Error()
	}

	var hash string
	if pc.JsonOutput {
		info := metadata.Info{}
		check(json.Unmarshal(body, &info))
		if writeErr != nil {
			errorMsg = writeErr.Error()
		}
		hash = info.Hash
		return StatusItem{
			Hash:      hash,
			Filename:  path,
			ErrorMsg:  errorMsg,
			MediaType: contentType,
			Size:      length,
			Head:      head,
		}
	} else {
		return StatusItem{
			Hash:      string(body),
			ErrorMsg:  errorMsg,
			Filename:  path,
			MediaType: contentType,
			Size:      length,
			Head:      head,
		}
	}
}

func (status *Status) validateURL(url string) string {
	if len(url) < 5 {
		status.ErrorMsg = "Url to short"
		return url
	}
	if url[len(url)-1] == '/' {
		url += "upload/"
	} else {
		url += "/upload/"
	}

	return url
}

func Upload(url string, file string, config PutConfig) *Status {
	status := &Status{}

	url = status.validateURL(url)
	if status.ErrorMsg != "" {
		return status
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

	status.upload(url, file, config)

	return status
}

// Upload without calculating hash on the client
func UploadNoHash(url string, file io.Reader, length int, config PutConfig) *Status {
	status := &Status{}

	// url = ValidateURL(url, status)
	// if status.ErrorMsg != "" {
	// 	return status
	// }

	s := config.UploadMultipart(url, file, length, "dummyfilename")

	// When uploading from reader we don't calculate local hash
	validate(s.Hash, s, status)

	return status
}

func (status *Status) upload(url string, file string, config PutConfig) {
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
		var hashes string = metadata.DirHeader
		for _, x := range entries {
			abs := filepath.Join(file, x.Name())
			abs = strings.ReplaceAll(abs, "\\", "/") // Replace Windows folder separator with slash
			status.upload(url, abs, config)
			hashes += metadata.DirEntry{
				Hash:     status.LastItem.Hash,
				Filename: status.LastItem.Filename,
				Size:     status.LastItem.Size,
				Head:     status.LastItem.Head,
			}.Line()
		}
		localhash, _ := config.AddressOf(strings.NewReader(hashes))
		uploadStatus := config.UploadMultipart(url+localhash, strings.NewReader(hashes), len(hashes), file)
		uploadStatus.MediaType = "inode/directory"
		validate(localhash, uploadStatus, status)
	} else {
		localhash, _ := config.AddressOfFile(file)
		uploadStatus := config.UploadFile(url+localhash, file)
		validate(localhash, uploadStatus, status)
	}
}

func validate(localhash string, uploadStatus StatusItem, status *Status) {
	if localhash != uploadStatus.Hash {
		errMsg := "Validation error: Upload checksum failed"
		if uploadStatus.ErrorMsg == "" {
			uploadStatus.ErrorMsg = errMsg
		} else {
			uploadStatus.ErrorMsg += "\n" + errMsg
		}
	}
	status.LastItem = uploadStatus

	if status.LastItem.ErrorMsg != "" {
		status.ErrorMsg += status.LastItem.ErrorMsg + "\n"
	}

	status.UploadedItems = append(status.UploadedItems, status.LastItem)
}
