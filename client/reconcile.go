package client

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Keeping a directory's children in step with what an import says belongs in
// it: superseded versions, retention drops, and detaching a child that moves to
// another directory. Split out of tag.go (§7.11).

// childEntry is one file child of a directory during reconciliation: its content
// hash, recorded display filename, and last-import date (for retention ordering).
type childEntry struct {
	Hash     string
	Filename string
	TagDate  time.Time
}

// supersededChildren returns the file children whose content hash this import did
// not place in the directory: a changed file uploads new bytes (a new hash, so the
// old hash is absent from want), and a removed/renamed file leaves its hash absent
// too. want is the set of content hashes this import placed in the directory.
//
// Reconciliation keys on the content hash, not the filename, because a hash can
// carry several filenames (identical bytes collapse to one content address) and be
// parented under several directories — the only unambiguous question is "did this
// import place this content here?".
func supersededChildren(children []childEntry, want map[string]bool) []childEntry {
	var out []childEntry
	for _, c := range children {
		if !want[c.Hash] {
			out = append(out, c)
		}
	}
	return out
}

// retentionDrops returns the oldest versions to remove so that at most keep
// remain, ordered by TagDate then Hash (ascending = oldest first). It returns
// nil when keep <= 0 (that case is handled by the no-history path) or when the
// count is already within the cap.
func retentionDrops(versions []childEntry, keep int) []childEntry {
	if keep <= 0 || len(versions) <= keep {
		return nil
	}
	sorted := make([]childEntry, len(versions))
	copy(sorted, versions)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TagDate.Equal(sorted[j].TagDate) {
			return sorted[i].TagDate.Before(sorted[j].TagDate)
		}
		return sorted[i].Hash < sorted[j].Hash
	})
	return sorted[:len(sorted)-keep]
}

// fileChildren reads the file children of a directory DirUID via a reverse
// parent-edge lookup. Unlike ReadTieDir it does NOT filter by media tie-type, so
// plain "file"/"unknown-file" children are included (they must be, or a
// superseded .txt would be invisible to reconciliation). Directory-marked
// children (subdirs) and children with no recorded filename are skipped.
func fileChildren(tie *TieClient, uid DirUID) ([]childEntry, error) {
	rows, _, err := tie.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Filter: string(RelParent), Expand: true})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []childEntry
	for _, row := range rows {
		if RowHas(row, RelType, str(TieDirectory)) {
			continue // subdir, not a file
		}
		// A hash can carry several filenames (identical bytes shared under
		// different names). Pick one deterministically for display and the
		// version location key; reconciliation itself keys on the hash.
		names := RowValues(row, RelFilename)
		if len(names) == 0 {
			continue // cannot reconcile a child with no recorded name
		}
		sort.Strings(names)
		out = append(out, childEntry{
			Hash:     row.Key,
			Filename: names[0],
			TagDate:  parseTagDate(RowFirst(row, RelTagDate)),
		})
	}
	return out, nil
}

// parentCount returns how many (hash,"parent",*) edges hash currently has.
func parentCount(tie *TieClient, hash string) (int, error) {
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return len(RowValues(row, RelParent)), nil
}

// detachChild removes the (hash,"parent",fromDir) edge. If that was the hash's
// last parent, its per-hash metadata (filename/name/tie-type/... and the tags it
// carries) is also deleted, since the content is no longer reachable from any
// directory. Shared content (still parented elsewhere) keeps its metadata.
// detachChild runs in the scoped client's collection.
func detachChild(tie *TieClient, hash string, fromDir DirUID) error {
	b := tie.NewBatch()
	b.Delete(hash, string(RelParent), str(fromDir))
	if _, err := tie.Batch(b); err != nil {
		return err
	}
	n, err := parentCount(tie, hash)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // still reachable from another directory; keep shared metadata
	}
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	meta := tie.NewBatch()
	for relation, values := range row.Attributes {
		for _, value2 := range values {
			meta.Delete(hash, relation, value2)
		}
	}
	if _, err := tie.Batch(meta); err != nil {
		return err
	}
	return nil
}

// reconcileDir makes dir's file children match what this import placed there
// (want: the set of content hashes placed in this directory). Superseded/orphaned
// children are recorded in the isolated "<Collection>_prev" history collection,
// keeping at most prevVersions of each (oldest dropped first); when prevVersions
// is 0 the old edge is removed outright and the content is garbage-collected if
// no directory still references it. Diagnostics go to stderr so stdout stays clean.
// reconcileDir runs in the scoped client's collection.
func reconcileDir(tie *TieClient, uid DirUID, want map[string]bool, prevVersions int) error {
	children, err := fileChildren(tie, uid)
	if err != nil {
		return err
	}
	superseded := supersededChildren(children, want)
	if len(superseded) == 0 {
		return nil
	}

	for _, c := range superseded {
		tie.log().Info("versioning superseded child", "filename", c.Filename, "hash", c.Hash, "dir", uid)
		if err := supersedeToPrev(tie, uid, c.Filename, c.Hash); err != nil {
			return err
		}
	}
	return nil
}

// nameFromFilename derives the display name from a filename by stripping its
// extension. TrimSuffix, not TrimRight: TrimRight treats its second argument
// as a character *set*, so "map.mp3" became "ma" and "bigpig.jpg" "bigpi".
func nameFromFilename(filename string) string {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if name == "" && filename != "" {
		return filename // dotfiles (".hidden") keep their whole name
	}
	return name
}

// RepairNames recomputes the `name` triple from `filename` for every file in
// the collection, fixing rows written by the old appendTagOps which used
// strings.TrimRight(filename, ext) — a character-set trim, not a suffix trim —
// so stems ending in a character that also appears in the extension were
// truncated ("map.mp3" → "ma"). It returns how many subjects were rewritten.
// An empty collection targets the bound one.
// RepairNames backfills missing display names in the scoped collection.
func (tc *TieClient) RepairNames() (int, error) {
	rows, err := tc.allOfType(str(TieFile))
	if err != nil {
		return 0, err
	}
	fixed := 0
	b := tc.NewBatch()
	for _, row := range rows {
		filename := RowFirst(row, RelFilename)
		if filename == "" {
			continue
		}
		want := nameFromFilename(filename)
		names := RowValues(row, RelName)
		if len(names) == 1 && names[0] == want {
			continue
		}
		if len(names) == 0 && want == "" {
			continue
		}
		b.Set(row.Key, string(RelName), []string{want})
		fixed++
		if len(b.Ops) >= historyFlush {
			if _, err := tc.Batch(b); err != nil {
				return fixed, err
			}
			b = tc.NewBatch()
		}
	}
	if len(b.Ops) > 0 {
		if _, err := tc.Batch(b); err != nil {
			return fixed, err
		}
	}
	return fixed, nil
}
