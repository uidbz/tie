package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/uidbz/tie/metadata"
)

// errChecksumMismatch is returned by putBlob when the body of an upload to
// /upload/{hash} hashes to something other than the requested hash. The
// handler maps it to 422; nothing is stored and no retention is recorded.
var errChecksumMismatch = errors.New("checksum mismatch")

// putBlob stores r.Body in st and returns the content address of what was
// fully received. When wantHash is non-empty the caller claims that address;
// if the received bytes hash differently the upload fails with
// errChecksumMismatch and nothing is stored.
//
// The body is staged in a temp file inside the destination shard directory,
// hashed while copying (no second read pass), fsynced, and then linked into
// place. os.Link fails with EEXIST when a concurrent upload of identical
// content won the race, which is success with existed=true — never a torn or
// interleaved file at the content address. Staging in the same directory
// means the final step never crosses filesystems (no EXDEV), unlike the old
// /tmp staging.
func putBlob(st *blobStore, body io.Reader, wantHash string) (hash string, existed, isDir bool, err error) {
	if wantHash != "" && !metadata.IsHexHash(wantHash) {
		return "", false, false, fmt.Errorf("invalid hash %q", wantHash)
	}

	// Dedup fast path: a claimed hash that is already on disk needs no body.
	// (Bytes are content-addressed, so presence at the address is proof.)
	if wantHash != "" {
		dest, err := PathFromHash(st.path, wantHash)
		if err != nil {
			return "", false, false, err
		}
		if _, err := os.Stat(dest); err == nil {
			return wantHash, true, isDirBlobAt(st, wantHash), nil
		}
	}

	// Stage inside the store so the link is same-filesystem. For a claimed
	// hash, stage in its shard dir; for a hashless upload, in the store root.
	stageDir := st.path
	if wantHash != "" {
		dest, err := st.makeDestinationPath(wantHash)
		if err != nil {
			return "", false, false, err
		}
		stageDir = filepath.Dir(dest)
	}
	tmp, err := os.CreateTemp(stageDir, ".upload-*")
	if err != nil {
		return "", false, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the link succeeded

	h, err := metadata.NewHash()
	if err != nil {
		tmp.Close()
		return "", false, false, err
	}
	// Capture the first bytes while hashing: the filehost needs to know whether
	// this blob is a tie-directory manifest so it can index it for the reaper.
	head := &headWriter{buf: make([]byte, len(metadata.DirHeader))}
	if _, err := io.Copy(io.MultiWriter(tmp, h, head), body); err != nil {
		tmp.Close()
		return "", false, false, err
	}
	isDir = metadata.IsDirHead(head.done())
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, false, err
	}

	hashHex := hex.EncodeToString(h.Sum(nil))
	if wantHash != "" && hashHex != wantHash {
		return "", false, false, errChecksumMismatch
	}

	dest, err := PathFromHash(st.path, hashHex)
	if err != nil {
		return "", false, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", false, false, err
	}
	if err := os.Link(tmpName, dest); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return "", false, false, err
		}
		existed = true // a concurrent upload of identical content won; fine
	}
	// Best-effort directory fsync so the link survives a power cut.
	if d, derr := os.Open(filepath.Dir(dest)); derr == nil {
		d.Sync()
		d.Close()
	}
	return hashHex, existed, isDir, nil
}

// headWriter keeps the first len(buf) bytes written to it, discarding the rest.
// It lets putBlob inspect a blob's header while hashing it in one pass.
type headWriter struct {
	n   int
	buf []byte
}

func (w *headWriter) Write(p []byte) (int, error) {
	if w.n < len(w.buf) {
		take := min(len(w.buf)-w.n, len(p))
		copy(w.buf[w.n:], p[:take])
		w.n += take
	}
	return len(p), nil
}

func (w *headWriter) done() []byte { return w.buf[:w.n] }

// isDirBlobAt reports whether an already-stored blob is a directory manifest
// (the dedup fast path: the bytes are on disk, the index may not know yet).
func isDirBlobAt(st *blobStore, hash string) bool {
	p, err := PathFromHash(st.path, hash)
	if err != nil {
		return false
	}
	return isDirBlob(p)
}
