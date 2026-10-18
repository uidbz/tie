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

	"github.com/uidbz/tie/io/fhclient"
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

// sink is where one downloaded file's bytes go, and how the download commits
// them. write receives the bytes; commit runs only once every byte has been
// read and hashed to the address it was requested under, and abort runs on any
// failure. The pair exists because verification finishes after the consumer has
// taken the bytes: writing straight to the final path would leave a truncated or
// tampered file sitting there, named exactly like a good one, while the checksum
// error travels back up to the caller. Callers that cache materialized blobs by
// content address (tie-gui, the mounts, `tie download`) treat "the file exists"
// as "the file is complete", so that leftover poisons them permanently.
type sink struct {
	write  io.Writer
	commit func() error
	abort  func()
}

// tieFunc opens the sink for one file of the tree, addressed by its path
// relative to the download root.
type tieFunc func(relPath string) (*sink, error)

// DownloadFile downloads sourceHash — a file, or a whole tree of directory
// manifests — into destination. Each file is staged beside its final name and
// moved into place only after its bytes hash to the address they were fetched
// under, so a cancelled request, a dropped connection, a short write or a
// tampering server leaves nothing at the final path.
func DownloadFile(client *http.Client, url string, sourceHash string, destination string, progress io.Writer) (err error) {
	return execForEach(client, url, sourceHash, stageInto(destination), "", progress, 0)
}

// tmpPrefix names the staged files a download leaves in the destination
// directory while bytes are in flight. The leading dot keeps them out of the
// way of a directory listing, and they are removed on every failure path.
const tmpPrefix = ".tie-dl-"

// stageInto returns the sink that stages each downloaded file in its destination
// directory and renames it into place on commit.
func stageInto(destination string) tieFunc {
	return func(relPath string) (*sink, error) {
		fullpath := filepath.Join(destination, relPath)
		dir := filepath.Dir(fullpath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, errors.New("Error making directories: " + err.Error())
		}
		tmp, err := os.CreateTemp(dir, tmpPrefix)
		if err != nil {
			return nil, err
		}
		tmpName := tmp.Name()
		return &sink{
			write: tmp,
			commit: func() error {
				// Flush the bytes before the name appears, then move the name.
				if err := tmp.Sync(); err != nil {
					tmp.Close()
					os.Remove(tmpName)
					return err
				}
				if err := tmp.Close(); err != nil {
					os.Remove(tmpName)
					return err
				}
				// os.CreateTemp is 0600; downloaded library files are ordinary
				// readable files, as os.Create would have made them.
				if err := os.Chmod(tmpName, 0644); err != nil {
					os.Remove(tmpName)
					return err
				}
				if err := os.Rename(tmpName, fullpath); err != nil {
					os.Remove(tmpName)
					return err
				}
				syncDir(dir)
				return nil
			},
			abort: func() {
				tmp.Close()
				os.Remove(tmpName)
			},
		}, nil
	}
}

// syncDir best-effort fsyncs a directory so the renamed entry itself survives a
// crash. Platforms that cannot open a directory for syncing just get nothing.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	f.Sync()
	f.Close()
}

// fetch GETs sourceHash and returns the body only on 200 OK, plus the size the
// filehost reported (-1 when it did not say). The caller must close the body.
//
// The content address is validated before it reaches the URL, so a crafted
// value cannot escape the intended endpoint. Status handling is fhclient's:
// a 404 is ErrNotFound, anything else non-200 is an error carrying the status.
func fetch(client *http.Client, url, sourceHash string) (io.ReadCloser, int64, error) {
	return fhclient.New(url, client).GetContent(sourceHash)
}

// readBlob fully reads a blob body, verifying it hashes to sourceHash. It is
// used for directory manifests and small reads where buffering is acceptable;
// large file downloads stream instead (see downloadVerified).
func readBlob(client *http.Client, url, sourceHash string) ([]byte, error) {
	body, _, err := fetch(client, url, sourceHash)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	data, err := io.ReadAll(body)
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

// TotalSize returns the number of file bytes a download of sourceHash would
// transfer, recursing into directory manifests. It lets callers size a progress
// bar before the transfer. For a single file it costs one HTTP request; for a
// directory it fetches each manifest (which are small) but no file bodies.
func TotalSize(client *http.Client, url string, sourceHash string) (int64, error) {
	return totalSize(client, url, sourceHash, 0)
}

func totalSize(client *http.Client, url string, sourceHash string, depth int) (int64, error) {
	var size int64
	if depth > maxDepth {
		return 0, fmt.Errorf("getlib: directory nesting exceeds %d levels (possible malicious manifest)", maxDepth)
	}

	body, size, err := fetch(client, url, sourceHash)
	if err != nil {
		return 0, err
	}
	defer body.Close()

	var head bytes.Buffer
	if _, err := io.CopyN(&head, body, int64(len(dirHeader))); err != nil {
		if err == io.EOF {
			// File shorter than the header prefix: its size is what we read.
			return int64(head.Len()), nil
		}
		return 0, err
	}

	if head.String() != dirHeader {
		// Regular file: trust Content-Length when the server provides it,
		// otherwise fall back to draining the body.
		if size >= 0 {
			return size, nil
		}
		n, err := io.Copy(io.Discard, body)
		return int64(head.Len()) + n, err
	}

	// Directory: sum entry sizes, recursing into sub-directories.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, body); err != nil {
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
	body, _, err := fetch(client, url, sourceHash)
	if err != nil {
		return false, err
	}
	defer body.Close()

	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, body, int64(len(dirHeader))); err != nil && err != io.EOF {
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
		return nil, errors.New("source is a directory, expected a file")
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

	body, _, err := fetch(client, url, sourceHash)
	if err != nil {
		return err
	}
	defer body.Close()

	// Peek the header prefix to classify file vs directory.
	var header bytes.Buffer
	if _, err := io.CopyN(&header, body, int64(len(dirHeader))); err != nil && err != io.EOF {
		return err
	}
	isDir := header.String() == dirHeader

	if !isDir {
		// Regular file: stream body -> hasher -> sink, so a multi-GB blob is
		// never fully buffered. verifyReader hashes every byte that flows, and
		// the sink is committed only once checkComplete agrees these are the
		// bytes this address names; any other outcome aborts the sink, which
		// removes what it staged. Verification therefore finishes before the
		// bytes become visible under their final name.
		hasher, err := metadata.NewHash()
		if err != nil {
			return err
		}
		full := io.MultiReader(bytes.NewReader(header.Bytes()), body)
		vr := &verifyReader{r: full, hasher: hasher, want: sourceHash}

		s, err := fn(relPath)
		if err != nil {
			return err
		}
		src := io.Reader(vr)
		if progress != nil {
			src = io.TeeReader(vr, progress)
		}
		if _, err := io.Copy(s.write, src); err != nil {
			s.abort()
			return err
		}
		if err := vr.checkComplete(); err != nil {
			s.abort()
			return err
		}
		return s.commit()
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
