package getlib

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/uidbz/tie/metadata"
)

// Used for cache path
const (
	shardLevels = 2 // directory nesting depth; must match tie-filehost
	dirWidth    = 2 // hex chars per level
)
const dirHeader = metadata.DirHeader

// maxDepth bounds directory-tree recursion. The filehost is untrusted: content
// addressing makes cycles impossible for an honest server, but a malicious one
// can serve a "directory" whose entries point back at an ancestor, which would
// otherwise recurse forever. 128 is far deeper than any real tree.
const maxDepth = 128

// ErrChecksum is returned when downloaded content does not hash to the address
// it was requested under, i.e. the server returned the wrong or tampered bytes.
var ErrChecksum = errors.New("getlib: downloaded content does not match its hash")

// tieFunc consumes one regular file's verified content and its path relative to
// the download root. execForEach invokes it for every file in the tree.
type tieFunc func(file io.Reader, relPath string) error

// get issues an HTTP GET using the provided client, falling back to
// http.DefaultClient when nil. Pass a custom client to control TLS behavior
// (e.g. InsecureSkipVerify for self-signed filehosts).
func get(client *http.Client, url string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return client.Get(url)
}

// fetch GETs sourceHash and returns the response only on 200 OK; the caller
// must close the body. sourceHash is validated as a content address so a
// crafted value cannot escape the intended endpoint.
func fetch(client *http.Client, url, sourceHash string) (*http.Response, error) {
	if !metadata.IsHexHash(sourceHash) {
		return nil, fmt.Errorf("getlib: invalid content hash %q", sourceHash)
	}
	resp, err := get(client, url+"/"+sourceHash)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}
	return resp, nil
}

// readBlob fully reads a blob body, verifying it hashes to sourceHash. It is
// used for directory manifests and small reads where buffering is acceptable;
// large file downloads stream instead (see downloadVerified).
func readBlob(client *http.Client, url, sourceHash string) ([]byte, error) {
	resp, err := fetch(client, url, sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	got, err := metadata.HashReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if got != sourceHash {
		return nil, ErrChecksum
	}
	return data, nil
}

func DownloadFile(client *http.Client, url string, sourceHash string, destination string, progress io.Writer) (err error) {
	writeFile := func(file io.Reader, relPath string) error {
		fullpath := filepath.Join(destination, relPath)
		dir := filepath.Dir(fullpath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return errors.New("Error making directories: " + err.Error())
		}
		dest, err := os.Create(fullpath)
		if err != nil {
			return err
		}
		defer dest.Close()
		if _, err := io.Copy(dest, file); err != nil {
			return err
		}
		return nil
	}

	return execForEach(client, url, sourceHash, writeFile, "", progress, 0)
}

// TotalSize returns the number of file bytes a download of sourceHash would
// transfer, recursing into directory manifests. It lets callers size a progress
// bar before the transfer. For a single file it costs one HTTP request; for a
// directory it fetches each manifest (which are small) but no file bodies.
func TotalSize(client *http.Client, url string, sourceHash string) (int64, error) {
	return totalSize(client, url, sourceHash, 0)
}

func totalSize(client *http.Client, url string, sourceHash string, depth int) (int64, error) {
	if depth > maxDepth {
		return 0, fmt.Errorf("getlib: directory nesting exceeds %d levels (possible malicious manifest)", maxDepth)
	}

	resp, err := fetch(client, url, sourceHash)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var head bytes.Buffer
	if _, err := io.CopyN(&head, resp.Body, int64(len(dirHeader))); err != nil {
		if err == io.EOF {
			// File shorter than the header prefix: its size is what we read.
			return int64(head.Len()), nil
		}
		return 0, err
	}

	if head.String() != dirHeader {
		// Regular file: trust Content-Length when the server provides it,
		// otherwise fall back to draining the body.
		if resp.ContentLength >= 0 {
			return resp.ContentLength, nil
		}
		n, err := io.Copy(io.Discard, resp.Body)
		return int64(head.Len()) + n, err
	}

	// Directory: sum entry sizes, recursing into sub-directories.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return 0, err
	}
	var total int64
	scanner := bufio.NewScanner(&buf)
	for scanner.Scan() {
		entry, ok := metadata.ParseDirLine(scanner.Text())
		if !ok {
			continue
		}
		if metadata.IsDirHead(entry.Head) {
			sub, err := totalSize(client, url, entry.Hash, depth+1)
			if err != nil {
				return 0, err
			}
			total += sub
		} else {
			total += int64(entry.Size)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

type Cache struct {
	CacheDir    string
	SizeLimit   int64  // Max bytes
	HistoryFile string // Contain hash and file sizes
}

func InitCache(customLocation string) Cache {
	c := Cache{}
	c.SizeLimit = 0
	historyFile := ".getlib-cache-history"

	if customLocation != "" {
		c.CacheDir = customLocation
	} else {
		switch runtime.GOOS {
		case "linux":
			c.CacheDir, _ = os.UserCacheDir()

		case "windows":
			c.CacheDir, _ = os.UserCacheDir()

		case "android":
			c.CacheDir = os.Getenv("FILESDIR")
		}
		c.CacheDir = filepath.Join(c.CacheDir, "tie-cache")
	}

	c.HistoryFile = filepath.Join(c.CacheDir, historyFile)

	return c
}

func (c *Cache) ReadFile(client *http.Client, url string, sourceHash string) (file io.Reader, err error) {
	if !metadata.IsHexHash(sourceHash) {
		return nil, fmt.Errorf("getlib: invalid content hash %q", sourceHash)
	}

	var dest string = c.CacheDir
	for i := 0; i < shardLevels*dirWidth; i += dirWidth {
		dest = filepath.Join(dest, sourceHash[i:i+dirWidth])
	}
	dir := dest
	dest = filepath.Join(dest, sourceHash)

	if _, err := os.Stat(dest); err == nil {
		return os.Open(dest)
	}

	// Cache miss: download and verify the whole blob before persisting, so a
	// truncated or tampered transfer never leaves a corrupt cache entry.
	data, err := readBlob(client, url, sourceHash)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(dir, dest, data); err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// writeFileAtomic writes data to a temp file in dir and renames it over dest,
// so a concurrent reader never observes a half-written cache entry.
func writeFileAtomic(dir, dest string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func IsDir(client *http.Client, url string, sourceHash string) (isDir bool, err error) {
	resp, err := fetch(client, url, sourceHash)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, resp.Body, int64(len(dirHeader))); err != nil && err != io.EOF {
		return false, err
	}
	return buf.String() == dirHeader, nil
}

// ReadFile returns the verified contents of sourceHash, erroring if it is a
// directory. The whole blob is buffered; use DownloadFile for large files.
func ReadFile(client *http.Client, url string, sourceHash string) (file io.Reader, err error) {
	b, err := ReadBytes(client, url, sourceHash)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ReadBytes returns the verified contents of sourceHash as a seekable reader,
// erroring if it is a directory.
func ReadBytes(client *http.Client, url string, sourceHash string) (b *bytes.Reader, err error) {
	data, err := readBlob(client, url, sourceHash)
	if err != nil {
		return nil, err
	}
	if len(data) >= len(dirHeader) && string(data[:len(dirHeader)]) == dirHeader {
		return nil, errors.New("Source is a directory; expected file.")
	}
	return bytes.NewReader(data), nil
}

// execForEach walks the tree rooted at sourceHash, invoking fn for every regular
// file with its content and path relative to relPath. File bodies are verified
// against their content address before fn sees them; a mismatch, truncation, or
// over-deep tree aborts with an error.
func execForEach(client *http.Client, url string, sourceHash string, fn tieFunc, relPath string, progress io.Writer, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("getlib: directory nesting exceeds %d levels (possible malicious manifest)", maxDepth)
	}

	resp, err := fetch(client, url, sourceHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Peek the header prefix to classify file vs directory.
	var header bytes.Buffer
	if _, err := io.CopyN(&header, resp.Body, int64(len(dirHeader))); err != nil && err != io.EOF {
		return err
	}
	isDir := header.String() == dirHeader

	if !isDir {
		// Regular file: stream body -> hasher -> Run, so a multi-GB blob is
		// never fully buffered. verifyReader hashes every byte the consumer
		// reads; content is verified before it is trusted.
		hasher, err := metadata.NewHash()
		if err != nil {
			return err
		}
		full := io.MultiReader(bytes.NewReader(header.Bytes()), resp.Body)
		vr := &verifyReader{r: full, hasher: hasher, want: sourceHash}

		var consumer io.Reader = vr
		if progress != nil {
			consumer = io.TeeReader(vr, progress)
		}
		if err := fn(consumer, relPath); err != nil {
			return err
		}
		return vr.checkComplete()
	}

	// Directory manifest: buffer (small) and verify the whole blob.
	manifest, err := readBlob(client, url, sourceHash)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(manifest))
	for scanner.Scan() {
		entry, ok := metadata.ParseDirLine(scanner.Text())
		if !ok {
			continue
		}
		if err := execForEach(client, url, entry.Hash, fn, filepath.Join(relPath, entry.Filename), progress, depth+1); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// verifyReader wraps a blob reader, hashing bytes as they flow to the consumer.
// After EOF, checkComplete confirms the accumulated hash matches the requested
// address. hashed guards against a consumer that stops reading early.
type verifyReader struct {
	r      io.Reader
	hasher hash.Hash
	want   string
	eof    bool
}

func (v *verifyReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if n > 0 {
		v.hasher.Write(p[:n])
	}
	if err == io.EOF {
		v.eof = true
	}
	return n, err
}

// checkComplete reports a checksum error unless the whole blob was read and its
// hash matches. It re-reads any tail the consumer skipped so partial reads
// cannot bypass verification.
func (v *verifyReader) checkComplete() error {
	if !v.eof {
		if _, err := io.Copy(io.Discard, v.r); err != nil {
			return err
		}
	}
	if hex.EncodeToString(v.hasher.Sum(nil)) != v.want {
		return ErrChecksum
	}
	return nil
}
