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

// reapOnce performs one mark-and-sweep pass. It deletes blobs whose retention
// has expired UNLESS they are still reachable from a live (non-expired)
// directory blob. It returns how many blobs were removed and how many expired
// blobs were kept because they were still referenced.
func reapOnce(now int64) (deleted, kept int) {
	expired := expiredSet(now)
	if len(expired) == 0 {
		return 0, 0
	}

	// Mark phase: protect every blob reachable from a live directory. Live
	// roots include permanent directories that are absent from the index, so
	// we enumerate all blobs on disk rather than only indexed ones. Protection
	// is transitive, so a live directory shields its whole sub-tree even if
	// individual descendants have expired.
	protected := map[string]bool{}
	visited := map[string]bool{}
	for _, h := range listBlobs() {
		if expired[h] {
			continue // an expired blob is not a live root
		}
		markReferences(h, protected, visited)
	}

	// Sweep phase.
	retention.mu.Lock()
	defer retention.mu.Unlock()
	changed := false
	for h := range expired {
		if protected[h] {
			kept++
			continue
		}
		path := PathFromHash(destination, h)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Error("reaper: error removing blob", "path", path, "err", err)
			continue
		}
		pruneShardDirs(path)
		delete(retention.entries, h)
		changed = true
		deleted++
	}
	if changed {
		if err := retention.save(); err != nil {
			slog.Error("reaper: error saving index", "err", err)
		}
	}
	return deleted, kept
}

// expiredSet snapshots the hashes whose retention has elapsed. Blobs absent
// from the index are permanent and never appear here.
func expiredSet(now int64) map[string]bool {
	retention.mu.Lock()
	defer retention.mu.Unlock()
	expired := map[string]bool{}
	for h, e := range retention.entries {
		if e.ExpiresAt != 0 && now >= e.ExpiresAt {
			expired[h] = true
		}
	}
	return expired
}

// listBlobs walks the datastore and returns every stored blob hash (the 64-char
// leaf file names), skipping the retention index and any stray files.
func listBlobs() []string {
	var hashes []string
	filepath.WalkDir(destination, func(path string, d fs.DirEntry, err error) error {
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
// the blob is missing or is not a tiedir. It reuses the hardened
// metadata.ParseDirLine so a crafted manifest cannot smuggle a bad hash.
func dirChildren(hash string) []string {
	f, err := os.Open(PathFromHash(destination, hash))
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
// deleted blob, walking up but never past the datastore root. A non-empty
// directory stops the walk.
func pruneShardDirs(blobPath string) {
	dir := filepath.Dir(blobPath)
	for dir != destination && len(dir) > len(destination) {
		if err := os.Remove(dir); err != nil {
			return // not empty (or gone) — stop pruning
		}
		dir = filepath.Dir(dir)
	}
}
