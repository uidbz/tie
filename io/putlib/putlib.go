// putlib project main.go
package putlib

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/h2non/filetype"

	"github.com/uidbz/tie/io/fhclient"
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
	// fh is the filehost client for this upload run: one client, so a whole
	// tree import shares its connections.
	fh *fhclient.Client
	// stack holds the real paths of the directories on the current recursion
	// path, so a symlink that points back up the tree is reported instead of
	// followed forever.
	stack map[string]bool
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

// UploadFile uploads the file at path under the content address hash.
func (pc *PutConfig) UploadFile(fh *fhclient.Client, hash, path string) StatusItem {
	f, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error(), Filename: path}
	}
	defer f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		return StatusItem{ErrorMsg: err.Error(), Filename: path}
	}

	return pc.UploadMultipart(fh, hash, f, int(fi.Size()), path)
}

// UploadMultipart uploads a reader under the content address hash. The head
// bytes are sampled here, before the body is consumed, because they are what
// classifies the entry in the parent manifest and what the media type is
// derived from; the transfer, the headers and the status handling are
// fhclient's.
func (pc *PutConfig) UploadMultipart(fh *fhclient.Client, hash string, f io.Reader, length int, path string) StatusItem {
	bufferedFileReader := bufio.NewReader(f)
	b, _ := bufferedFileReader.Peek(metadata.MagicNumber) // returns available bytes even for short files
	head := append([]byte(nil), b...)
	contentType := "application/octet-stream"
	if t, err := filetype.Get(b); err == nil {
		contentType = t.MIME.Value
	}

	answer, err := fh.Put(hash, bufferedFileReader, int64(length), fhclient.PutOptions{
		MediaType:  contentType,
		Retention:  pc.Retention,
		OwnerToken: pc.OwnerToken,
		Store:      pc.Store,
		Progress:   pc.Progress,
	})
	if err != nil {
		return StatusItem{
			ErrorMsg:  err.Error(),
			Filename:  path,
			MediaType: contentType,
			Size:      length,
			Head:      head,
		}
	}
	return StatusItem{
		Hash:      answer,
		Filename:  path,
		MediaType: contentType,
		Size:      length,
		Head:      head,
	}
}

// alreadyStored asks the filehost whether it holds this content address at the
// expected size (HEAD /{hash}) before streaming a body: the server answers an
// upload of existing content without reading it, but the client still pays to
// read the file and send every byte — re-importing a library used to move the
// whole library across the wire for nothing (§5.2 item 2).
//
// The answer must also report the same length we are about to send: a 200 from
// something that is not a tie filehost (a proxy, a stub) must never be taken as
// "stored", or the manifest would reference a blob that does not exist. Any
// other answer — 404, a 403 from a read-restricted token, a transport error —
// means "upload it", so this can only ever save work.
func (status *Status) alreadyStored(hash string, size int64) bool {
	ok, got, err := status.fh.Stat(hash)
	return err == nil && ok && got == size
}

// existingItem describes a blob the filehost already has, so the caller can
// report it (and write its manifest entry) without reading the whole file. The
// head bytes still have to be sampled: they are what classifies an entry as a
// directory and let the media type be recomputed downstream.
func existingItem(hash, path, mediaType string, size int, head []byte) StatusItem {
	return StatusItem{Hash: hash, Filename: path, MediaType: mediaType, Size: size, Head: head}
}

// headOf returns the first metadata.MagicNumber bytes of s, the same prefix
// UploadMultipart would have peeked.
func headOf(s string) []byte {
	if len(s) <= metadata.MagicNumber {
		return []byte(s)
	}
	return []byte(s[:metadata.MagicNumber])
}

// fileHead samples a file's leading bytes and size without reading it whole.
func fileHead(path string) (head []byte, size int, mediaType string) {
	fi, err := os.Stat(path)
	if err == nil {
		size = int(fi.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, size, ""
	}
	defer f.Close()
	buf := make([]byte, metadata.MagicNumber)
	n, _ := io.ReadFull(f, buf)
	head = buf[:n]
	if t, err := filetype.Get(head); err == nil {
		mediaType = t.MIME.Value
	}
	return head, size, mediaType
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
	status.fh = fhclient.New(strings.TrimSuffix(url, "upload/"), config.Client)
	status.fh.Store = config.Store

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

	status.upload(file, config)

	return status
}

func (status *Status) upload(file string, config PutConfig) {
	// Never leave the previous item's hash behind: the caller reads LastItem to
	// build a manifest entry, and a stale one would point a child at the wrong
	// blob.
	status.LastItem = StatusItem{}

	fi, errStat := os.Lstat(file)
	if errStat != nil {
		status.ErrorMsg += "Error stat file: " + file + errStat.Error()
		return
	}

	// Classify a symlink by its target. Lstat reports a link to a directory as
	// a non-directory, and hashing it then failed with "is a directory" — which
	// aborted the whole tree import (§5.1 L15).
	isDir := fi.IsDir()
	if fi.Mode()&os.ModeSymlink != 0 {
		tfi, err := os.Stat(file)
		if err != nil {
			status.ErrorMsg += "Error following symlink " + file + ": " + err.Error() + "\n"
			return
		}
		isDir = tfi.IsDir()
	}

	if isDir {
		// Following links means a link back up the tree would recurse forever.
		// The real paths of the directories on the current path are the stack
		// that a link must not re-enter.
		if status.stack == nil {
			status.stack = map[string]bool{}
		}
		real, err := filepath.EvalSymlinks(file)
		if err != nil {
			status.ErrorMsg += "Error resolving " + file + ": " + err.Error() + "\n"
			return
		}
		if status.stack[real] {
			status.ErrorMsg += "Skipping " + file + ": symlink loop back to " + real + "\n"
			return
		}
		status.stack[real] = true
		defer delete(status.stack, real)

		entries, err := os.ReadDir(file)
		if err != nil {
			// Abort rather than upload a partial manifest that would look
			// complete but silently omit unreadable entries.
			status.ErrorMsg += "Error reading directory " + file + ": " + err.Error() + "\n"
			return
		}
		var hashes string = metadata.DirHeader
		for _, x := range entries {
			name := x.Name()
			if !metadata.ValidEntryName(name) {
				// This name cannot be represented in a manifest line at all (a
				// backslash is a path separator on Windows). Uploading it would
				// store a blob that every reader drops from the manifest, and a
				// blob no manifest references is one the reaper may delete.
				// Refuse it where the user can see it instead (§4.8).
				status.ErrorMsg += "Cannot store " + filepath.Join(file, name) + ": this filename cannot appear in a tie directory\n"
				return
			}
			abs := filepath.Join(file, name)
			status.upload(abs, config)
			if status.ErrorMsg != "" {
				return
			}
			// Store only the child's own name; the tree's location is supplied
			// by the caller at checkout. This keeps a directory's hash a pure
			// function of its contents, so identical trees dedupe regardless of
			// where they were uploaded from.
			hashes += metadata.DirEntry{
				Hash:     status.LastItem.Hash,
				Filename: name,
				Size:     status.LastItem.Size,
				Head:     status.LastItem.Head,
			}.Line()
		}
		localhash, err := config.AddressOf(strings.NewReader(hashes))
		if err != nil {
			status.ErrorMsg += "Error hashing directory " + file + ": " + err.Error() + "\n"
			return
		}
		if status.alreadyStored(localhash, int64(len(hashes))) {
			validate(localhash, existingItem(localhash, file, "inode/directory", len(hashes), headOf(hashes)), status)
			return
		}
		uploadStatus := config.UploadMultipart(status.fh, localhash, strings.NewReader(hashes), len(hashes), file)
		uploadStatus.MediaType = "inode/directory"
		validate(localhash, uploadStatus, status)
	} else {
		localhash, err := config.AddressOfFile(file)
		if err != nil {
			status.ErrorMsg += "Error hashing file " + file + ": " + err.Error() + "\n"
			return
		}
		head, headSize, mediaType := fileHead(file)
		if status.alreadyStored(localhash, int64(headSize)) {
			validate(localhash, existingItem(localhash, file, mediaType, headSize, head), status)
			return
		}
		uploadStatus := config.UploadFile(status.fh, localhash, file)
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
