package main

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/uidbz/tie/metadata"
)

// dirIndexFileName is the reserved per-store file listing the blobs that are
// tie-directory manifests. Like the retention index, the leading dot keeps it
// out of the 2-hex shard dirs and the 64-char hash validation rejects it as a
// blob name, so it can never collide with stored content.
const dirIndexFileName = ".tie-dirs.txt"

// dirIndex is the set of directory blobs in one store. The reaper's mark phase
// needs every live directory's child list; before this it found them by
// opening EVERY blob in the store on every pass (§5.2 item 9). The filehost
// already knows which uploads are manifests (the tiedir-v header), so it
// records them here and the reaper walks only these.
//
// Line-based, one hash per line: appending an upload is a single small write,
// and a torn trailing line from a crash is simply ignored (the next pass's
// bootstrap or a fresh upload repairs it).
type dirIndex struct {
	mu   sync.Mutex
	path string
	dirs map[string]bool
}

// loadDirIndex reads the index. A missing file is not an error: it yields an
// empty index, and the caller bootstraps it by scanning the store once (a
// store predating this file has no index to read).
func loadDirIndex(root string) (*dirIndex, bool, error) {
	d := &dirIndex{path: filepath.Join(root, dirIndexFileName), dirs: map[string]bool{}}
	data, err := os.ReadFile(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, false, nil
		}
		return d, false, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line != "" && metadata.IsHexHash(line) {
			d.dirs[line] = true
		}
	}
	return d, true, nil
}

// add records one directory blob. It returns true when the hash was new, and
// appends it to the file so the record survives a restart.
func (d *dirIndex) add(hash string) bool {
	if !metadata.IsHexHash(hash) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dirs[hash] {
		return false
	}
	d.dirs[hash] = true
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Error("opening dir index", "path", d.path, "err", err)
		return true
	}
	defer f.Close()
	if _, err := f.WriteString(hash + "\n"); err != nil {
		slog.Error("appending to dir index", "path", d.path, "err", err)
		return true
	}
	f.Sync()
	return true
}

// remove forgets a directory blob (it was reaped), keeping the index from
// growing past the live set.
func (d *dirIndex) remove(hash string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.dirs[hash] {
		return
	}
	delete(d.dirs, hash)
	d.rewriteLocked()
}

// rewriteLocked persists the current set. Callers must hold mu.
func (d *dirIndex) rewriteLocked() {
	tmp := d.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Error("writing dir index", "path", tmp, "err", err)
		return
	}
	w := bufio.NewWriter(f)
	for h := range d.dirs {
		w.WriteString(h + "\n")
	}
	if err := w.Flush(); err != nil {
		f.Close()
		slog.Error("writing dir index", "path", tmp, "err", err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		slog.Error("syncing dir index", "path", tmp, "err", err)
		return
	}
	f.Close()
	if err := os.Rename(tmp, d.path); err != nil {
		slog.Error("replacing dir index", "path", d.path, "err", err)
	}
}

// all returns a snapshot of the recorded directory hashes.
func (d *dirIndex) all() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.dirs))
	for h := range d.dirs {
		out = append(out, h)
	}
	return out
}

// bootstrap fills the index by scanning the store once. A store created before
// this index existed has no file to read, so the first pass pays the full scan
// the reaper used to pay every pass; later passes read the file.
func (d *dirIndex) bootstrap(root string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, h := range listBlobs(root) {
		if d.dirs[h] {
			continue
		}
		p, err := PathFromHash(root, h)
		if err != nil {
			continue
		}
		if isDirBlob(p) {
			d.dirs[h] = true
		}
	}
	d.rewriteLocked()
	slog.Info("dir index bootstrapped", "path", d.path, "dirs", len(d.dirs))
}

// repair picks up directory blobs the index missed: an upload that crashed
// between linking the blob and appending its line, or a torn trailing line from
// a crash mid-append. Only blobs newer than the index file can be missing, so
// this costs a stat per blob and an open for the handful that are new.
func (d *dirIndex) repair(root string) {
	info, err := os.Stat(d.path)
	if err != nil {
		return // no index file: bootstrap covers it
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, h := range listBlobsModifiedSince(root, info.ModTime()) {
		if d.dirs[h] {
			continue
		}
		p, err := PathFromHash(root, h)
		if err != nil {
			continue
		}
		if isDirBlob(p) {
			d.dirs[h] = true
		}
	}
	d.rewriteLocked()
}

// isDirBlob reports whether the file at path starts with the tie-directory
// header. Only the first bytes are read.
func isDirBlob(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(metadata.DirHeader))
	n, _ := f.Read(head)
	return metadata.IsDirHead(head[:n])
}
