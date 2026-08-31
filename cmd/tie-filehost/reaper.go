package main

import (
	"bufio"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/uidbz/tie/metadata"
)

// startReaper launches the background sweep of expired blobs. interval <= 0
// disables reaping entirely.
func startReaper(interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			deleted, kept := reapOnce(time.Now().Unix())
			slog.Info("reaper pass complete", "removed", deleted, "keptReferenced", kept)
		}
	}()
}

// reapOnce performs one mark-and-sweep pass across all stores. It deletes blobs
// whose retention has expired UNLESS they are still reachable from a live
// (non-expired) directory blob in ANY store — a directory in one store may
// reference children in another, so protection is computed globally. It returns
// how many blobs were removed and how many expired blobs were kept because they
// were still referenced.
func reapOnce(now int64) (deleted, kept int) {
	// Union the expired sets, remembering which store owns each expired hash.
	expired := map[string]*blobStore{}
	for _, s := range stores {
		for h := range s.expiredSet(now) {
			expired[h] = s
		}
	}
	if len(expired) == 0 {
		return 0, 0
	}

	// Mark phase: protect every blob reachable from a live directory in any
	// store. Live roots include permanent directories absent from the index, so
	// we enumerate all blobs on disk rather than only indexed ones. dirChildren
	// resolves children via findBlob, so protection follows cross-store
	// references and shields a live directory's whole sub-tree transitively.
	protected := map[string]bool{}
	visited := map[string]bool{}
	for _, s := range stores {
		for _, h := range listBlobs(s.path) {
			if _, isExpired := expired[h]; isExpired {
				continue // an expired blob is not a live root
			}
			markReferences(h, protected, visited)
		}
	}

	// Sweep phase, grouped per store so each index is saved once.
	toDelete := map[*blobStore][]string{}
	for h, s := range expired {
		if protected[h] {
			kept++
			continue
		}
		toDelete[s] = append(toDelete[s], h)
	}
	for s, hashes := range toDelete {
		s.retention.mu.Lock()
		changed := false
		for _, h := range hashes {
			path := PathFromHash(s.path, h)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Error("reaper: error removing blob", "path", path, "err", err)
				continue
			}
			pruneShardDirs(s.path, path)
			delete(s.retention.entries, h)
			changed = true
			deleted++
		}
		if changed {
			if err := s.retention.save(); err != nil {
				slog.Error("reaper: error saving index", "store", s.name, "err", err)
			}
		}
		s.retention.mu.Unlock()
	}
	return deleted, kept
}

// expiredSet snapshots the hashes in this store whose retention has elapsed.
// Blobs absent from the index are permanent and never appear here.
func (s *blobStore) expiredSet(now int64) map[string]bool {
	s.retention.mu.Lock()
	defer s.retention.mu.Unlock()
	expired := map[string]bool{}
	for h, e := range s.retention.entries {
		if e.ExpiresAt != 0 && now >= e.ExpiresAt {
			expired[h] = true
		}
	}
	return expired
}

// listBlobs walks a store root and returns every stored blob hash (the 64-char
// leaf file names), skipping the retention index and any stray files.
func listBlobs(root string) []string {
	var hashes []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if name := d.Name(); metadata.IsHexHash(name) {
			hashes = append(hashes, name)
		}
		return nil
	})
	return hashes
}

// markReferences protects every blob reachable from hash via directory
// references. hash itself is not added (a live root is never deleted); its
// descendants are, transitively, regardless of their own expiry.
func markReferences(hash string, protected, visited map[string]bool) {
	if visited[hash] {
		return
	}
	visited[hash] = true
	for _, child := range dirChildren(hash) {
		protected[child] = true
		markReferences(child, protected, visited)
	}
}

// dirChildren returns the child hashes listed in a directory blob, or nil if
// the blob is missing or is not a tiedir. It locates the blob in whichever
// store holds it (findBlob), so a directory can reference children across
// stores. It reuses the hardened metadata.ParseDirLine so a crafted manifest
// cannot smuggle a bad hash.
func dirChildren(hash string) []string {
	st, ok := findBlob(hash)
	if !ok {
		return nil
	}
	f, err := os.Open(PathFromHash(st.path, hash))
	if err != nil {
		return nil
	}
	defer f.Close()

	r := bufio.NewReader(f)
	header := make([]byte, len(metadata.DirHeader))
	if _, err := io.ReadFull(r, header); err != nil {
		return nil
	}
	if string(header) != metadata.DirHeader {
		return nil
	}

	var children []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		entry, ok := metadata.ParseDirLine(scanner.Text())
		if !ok {
			continue
		}
		children = append(children, entry.Hash)
	}
	return children
}

// pruneShardDirs removes the now-possibly-empty shard directories that held a
// deleted blob, walking up but never past the store root. A non-empty directory
// stops the walk.
func pruneShardDirs(root, blobPath string) {
	dir := filepath.Dir(blobPath)
	for dir != root && len(dir) > len(root) {
		if err := os.Remove(dir); err != nil {
			return // not empty (or gone) — stop pruning
		}
		dir = filepath.Dir(dir)
	}
}
