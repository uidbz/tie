// putlib project main.go
package putlib

import (
	"bufio"
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

	"github.com/uidbz/tie/metadata"
)

type PutConfig struct {
	PathToWorkdir bool
	// Client is the HTTP client used for uploads. When nil, http.DefaultClient
	// is used. Set it to control TLS behavior (e.g. InsecureSkipVerify).
	Client *http.Client
	// Progress, when non-nil, receives a Write for every chunk of the request
	// body sent, so callers can render an upload progress bar. For a directory
	// that is the file bytes plus one manifest per directory, so the total can
	// exceed a bar sized from file sizes alone. Writes are best-effort: an
	// error from the writer is discarded and never aborts the upload.
	Progress io.Writer
	// Retention, when non-empty, is sent as the Tie-Retention header on every
	// upload: a Go duration (e.g. "72h") or "infinite". Empty means the server
	// default (permanent).
	Retention string
	// OwnerToken, when non-empty, is sent as the Tie-Owner header so the owner
	// can later change the blob's retention. It is a shared secret, not stored
	// in plaintext on the server.
	OwnerToken string
	// Store, when non-empty, is sent as the Tie-Store header to select which
	// physical store on a multi-store filehost receives the blob. Empty targets
	// the filehost's default store.
	Store string
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

func (pc *PutConfig) AddressOfFile(file string) (string, error) { // function to compute address based on content
	fsocket, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer fsocket.Close()

	return pc.AddressOf(fsocket)
}

func (pc *PutConfig) AddressOf(input io.Reader) (string, error) { // function to compute address based on content
	return metadata.HashReader(input)
}

func (pc *PutConfig) UploadFile(url string, path string) StatusItem {
	f, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error()}
	}
	defer f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error()}
	}

	return pc.UploadMultipart(url, f, int(fi.Size()), path)
}

// progressSink wraps a progress writer to discard its errors. Upload progress
// is cosmetic, and the total written can legitimately exceed the bar's max: a
// directory upload streams one tiedir manifest per directory on top of the file
// bytes the max was sized from, which makes the bar return "current number
// exceeds max". io.TeeReader would propagate that error into the request body
// read and tear down the in-flight stream, so swallow it.
type progressSink struct{ w io.Writer }

func (s progressSink) Write(p []byte) (int, error) {
	_, _ = s.w.Write(p)
	return len(p), nil
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

	var reqBody io.Reader = bufferedFileReader
	if pc.Progress != nil {
		reqBody = io.TeeReader(bufferedFileReader, progressSink{pc.Progress})
	}
	req, err := http.NewRequest(http.MethodPut, url, reqBody)
	check(err)

	contentType := "application/octet-stream"
	b, _ := bufferedFileReader.Peek(metadata.MagicNumber) // returns available bytes even for short files
	head := append([]byte(nil), b...)
	if t, err := filetype.Get(b); err == nil {
		contentType = t.MIME.Value
	}
	req.Header.Add("Content-Type", contentType)
	req.Header.Add("Content-Length", strconv.Itoa(length))
	if pc.Retention != "" {
		req.Header.Add("Tie-Retention", pc.Retention)
	}
	if pc.OwnerToken != "" {
		req.Header.Add("Tie-Owner", pc.OwnerToken)
	}
	if pc.Store != "" {
		req.Header.Add("Tie-Store", pc.Store)
	}

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
		// A non-2xx body is an error message (401/403 from auth, 400 for a bad
		// store/retention, 5xx), not a hash. Surface it instead of letting the
		// caller's checksum compare turn it into a misleading "checksum failed".
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := strings.TrimSpace(string(body))
			if msg == "" {
				msg = http.StatusText(resp.StatusCode)
			}
			check(fmt.Errorf("filehost returned %d: %s", resp.StatusCode, msg))
			body = nil
		}
	}

	var errorMsg string
	if writeErr != nil {
		errorMsg = writeErr.Error()
	}

	return StatusItem{
		Hash:      string(body),
		ErrorMsg:  errorMsg,
		Filename:  path,
		MediaType: contentType,
		Size:      length,
		Head:      head,
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
		if err != nil {
			return &Status{ErrorMsg: "Error stat file: " + file + " : " + err.Error()}
		}
		if fi.IsDir() {
			dir = file
			file = "."
		} else {
			dir = filepath.Dir(file)
			file = filepath.Base(file)
		}
		if err := os.Chdir(dir); err != nil {
			return &Status{ErrorMsg: "Error changing dir to:" + dir + " : " + err.Error()}
		}
	}

	status.upload(url, file, config)

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
			// Abort rather than upload a partial manifest that would look
			// complete but silently omit unreadable entries.
			status.ErrorMsg += "Error reading directory " + file + ": " + err.Error() + "\n"
			return
		}
		var hashes string = metadata.DirHeader
		for _, x := range entries {
			abs := filepath.Join(file, x.Name())
			status.upload(url, abs, config)
			if status.ErrorMsg != "" {
				return
			}
			// Store only the child's own name; the tree's location is supplied
			// by the caller at checkout. This keeps a directory's hash a pure
			// function of its contents, so identical trees dedupe regardless of
			// where they were uploaded from.
			hashes += metadata.DirEntry{
				Hash:     status.LastItem.Hash,
				Filename: x.Name(),
				Size:     status.LastItem.Size,
				Head:     status.LastItem.Head,
			}.Line()
		}
		localhash, err := config.AddressOf(strings.NewReader(hashes))
		if err != nil {
			status.ErrorMsg += "Error hashing directory " + file + ": " + err.Error() + "\n"
			return
		}
		uploadStatus := config.UploadMultipart(url+localhash, strings.NewReader(hashes), len(hashes), file)
		uploadStatus.MediaType = "inode/directory"
		validate(localhash, uploadStatus, status)
	} else {
		localhash, err := config.AddressOfFile(file)
		if err != nil {
			status.ErrorMsg += "Error hashing file " + file + ": " + err.Error() + "\n"
			return
		}
		uploadStatus := config.UploadFile(url+localhash, file)
		validate(localhash, uploadStatus, status)
	}
}

func validate(localhash string, uploadStatus StatusItem, status *Status) {
	// A checksum mismatch is only meaningful when the upload itself succeeded;
	// otherwise the transport/HTTP error already explains the missing hash.
	if uploadStatus.ErrorMsg == "" && localhash != uploadStatus.Hash {
		uploadStatus.ErrorMsg = "Validation error: Upload checksum failed"
	}
	status.LastItem = uploadStatus

	if status.LastItem.ErrorMsg != "" {
		status.ErrorMsg += status.LastItem.ErrorMsg + "\n"
	}

	status.UploadedItems = append(status.UploadedItems, status.LastItem)
}
