package main

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/uidbz/tie/metadata"
)

// dirIndexFileName is the reserved per-store file listing the blobs that are
// tie-directory manifests. Like the retention index, the leading dot keeps it
// out of the 2-hex shard dirs and the 64-char hash validation rejects it as a
// blob name, so it can never collide with stored content.
const dirIndexFileName = ".tie-dirs.txt"

// dirIndexComplete marks an index that has seen a full scan of the store.
// Without it the set is only what uploads happened to record since the file was
// created, and the reaper must not trust it: a directory blob the scan never
// reached would leave its children unprotected. Lines starting with # are
// ignored by the hash check, so an older filehost simply skips the marker.
const dirIndexComplete = "#complete"

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
	mu    sync.Mutex
	path  string
	dirs  map[string]bool
	ready bool // a full scan has completed and the marker is on disk
}

// loadDirIndex reads the index. A missing file is not an error: it yields an
// empty index that is not ready, and the reaper bootstraps it before its first
// mark phase (a store predating this file has no index to read). A file
// without the completion marker is a scan interrupted by a crash, so it is
// likewise not ready — trusting it would protect only part of the live tree.
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
		switch {
		case line == dirIndexComplete:
			d.ready = true
		case line != "" && metadata.IsHexHash(line):
			d.dirs[line] = true
		}
	}
	return d, true, nil
}

// has reports whether a hash is recorded.
func (d *dirIndex) has(hash string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dirs[hash]
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

// rewriteLocked persists the current set. Callers must hold mu. The completion
// marker travels with every rewrite, so a remove (or an add) cannot drop the
// fact that a full scan happened.
func (d *dirIndex) rewriteLocked() {
	tmp := d.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Error("writing dir index", "path", tmp, "err", err)
		return
	}
	w := bufio.NewWriter(f)
	if d.ready {
		w.WriteString(dirIndexComplete + "\n")
	}
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

// bootstrap fills the index by scanning the store once, then records that the
// scan completed. A store created before this index existed has no file to
// read, and a scan interrupted by a crash leaves a file without the marker;
// both cases land here.
//
// It runs from the reaper, not from start-up: the scan opens every blob in the
// store, which on a large cold one is minutes of work, and doing it before the
// listener opens means the filehost answers no request — and no health check —
// while it counts. Each hash found is appended as it is discovered, so an
// interrupted scan keeps the work it did instead of starting over, and the
// mutex is taken per entry so uploads are not queued behind the walk.
func (d *dirIndex) bootstrap(root string) {
	start := time.Now()
	scanned, found := 0, 0
	for _, h := range listBlobs(root) {
		scanned++
		if d.has(h) {
			continue
		}
		p, err := PathFromHash(root, h)
		if err != nil {
			continue
		}
		if isDirBlob(p) {
			d.add(h)
			found++
		}
	}
	d.mu.Lock()
	d.ready = true
	d.rewriteLocked()
	d.mu.Unlock()
	slog.Info("dir index built", "path", d.path, "blobs", scanned, "dirs", found, "took", time.Since(start).Round(time.Millisecond))
}

// repair picks up directory blobs the index missed: an upload that crashed
// between linking the blob and appending its line, or a torn trailing line from
// a crash mid-append. Only blobs newer than the index file can be missing, so
// this costs a stat per blob and an open for the handful that are new.
//
// It is only sound on a ready index: without a completed scan, blobs older than
// the index file may never have been looked at, and this cheap path would
// report them as absent.
func (d *dirIndex) repair(root string) {
	if !d.ready {
		return
	}
	info, err := os.Stat(d.path)
	if err != nil {
		return // no index file: bootstrap covers it
	}
	start := time.Now()
	scanned, found := 0, 0
	for _, h := range listBlobsModifiedSince(root, info.ModTime()) {
		scanned++
		if d.has(h) {
			continue
		}
		p, err := PathFromHash(root, h)
		if err != nil {
			continue
		}
		if isDirBlob(p) {
			d.add(h)
			found++
		}
	}
	if found > 0 {
		slog.Info("dir index repaired", "path", d.path, "checked", scanned, "dirs", found, "took", time.Since(start).Round(time.Millisecond))
	}
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
