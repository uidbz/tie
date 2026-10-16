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
func startReaper(interval time.Duration, srv *server) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			deleted, kept := srv.reapOnce(time.Now().Unix())
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
func (srv *server) reapOnce(now int64) (deleted, kept int) {
	passStart := time.Now()
	// Union the expired sets, remembering which store owns each expired hash.
	expired := map[string]*blobStore{}
	for _, st := range srv.stores {
		for h := range st.expiredSet(now) {
			expired[h] = st
		}
	}
	if len(expired) == 0 {
		return 0, 0
	}

	// Repair first: a manifest placed in the store out-of-band (a restore, an
	// older filehost, a crash that lost the appended line) must still protect
	// its children. This is a stat per blob and an open only for the handful
	// newer than the index file — the cheap form of the scan this used to do
	// every pass.
	for _, st := range srv.stores {
		st.dirs.repair(st.path)
	}

	// Mark phase: protect every blob reachable from a live directory in any
	// store. Only directory manifests have children, and every upload records
	// whether it is one (dirIndex), so the walk reads that set instead of
	// opening every blob in the store — the cost the review measured at one
	// open per blob per pass. dirChildren resolves children via findBlob, so
	// protection follows cross-store references and shields a live directory's
	// whole sub-tree transitively.
	protected := map[string]bool{}
	visited := map[string]bool{}
	for _, st := range srv.stores {
		for _, h := range st.dirs.all() {
			if _, isExpired := expired[h]; isExpired {
				continue // an expired directory is not a live root
			}
			srv.markReferences(h, protected, visited)
		}
	}
	// A directory blob uploaded *during* this pass was not in the walk above,
	// yet its children are in the expired snapshot and would be swept out from
	// under it (TOCTOU). Re-walk for blobs whose mtime postdates the pass and
	// mark their references too.
	for _, st := range srv.stores {
		for _, h := range listBlobsModifiedSince(st.path, passStart) {
			srv.markReferences(h, protected, visited)
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
	for st, hashes := range toDelete {
		deleted += sweepStore(st, hashes, now)
		// Forget reaped manifests so the index tracks the live set.
		for _, h := range hashes {
			st.dirs.remove(h)
		}
	}
	return deleted, kept
}

// sweepStore deletes the given expired hashes from one store and prunes their
// index entries, saving the index once. Each hash is re-checked under the
// index lock before removal: between the expiredSet snapshot and this sweep a
// client may have re-uploaded the blob with longer or infinite retention
// (TOCTOU), and deleting those bytes is data loss. A hash whose entry is gone,
// permanent, or no longer expired is skipped.
func sweepStore(s *blobStore, hashes []string, now int64) (deleted int) {
	s.retention.mu.Lock()
	defer s.retention.mu.Unlock()
	changed := false
	for _, h := range hashes {
		if e, ok := s.retention.entries[h]; !ok || e.ExpiresAt == 0 || now < e.ExpiresAt {
			continue // re-extended or made permanent since the snapshot
		}
		path, err := PathFromHash(s.path, h)
		if err != nil {
			slog.Error("reaper: malformed hash in index", "hash", h, "err", err)
			continue
		}
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
	return deleted
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

// listBlobsModifiedSince returns blob hashes in the store whose file mtime is
// at or after since — i.e. blobs written during the current reaper pass.
func listBlobsModifiedSince(root string, since time.Time) []string {
	var hashes []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !metadata.IsHexHash(name) {
			return nil
		}
		if info, err := d.Info(); err == nil && !info.ModTime().Before(since) {
			hashes = append(hashes, name)
		}
		return nil
	})
	return hashes
}

// markReferences protects every blob reachable from hash via directory
// references. hash itself is not added (a live root is never deleted); its
// descendants are, transitively, regardless of their own expiry.
func (srv *server) markReferences(hash string, protected, visited map[string]bool) {
	if visited[hash] {
		return
	}
	visited[hash] = true
	for _, child := range srv.dirChildren(hash) {
		protected[child] = true
		srv.markReferences(child, protected, visited)
	}
}

// dirChildren returns the child hashes listed in a directory blob, or nil if
// the blob is missing or is not a tiedir. It locates the blob in whichever
// store holds it (findBlob), so a directory can reference children across
// stores. It reuses the hardened metadata.ParseDirLine so a crafted manifest
// cannot smuggle a bad hash.
func (srv *server) dirChildren(hash string) []string {
	st, ok := srv.findBlob(hash)
	if !ok {
		return nil
	}
	path, err := PathFromHash(st.path, hash)
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
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
