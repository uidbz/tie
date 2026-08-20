package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// prevCollectionSuffix is appended to the live collection name to form the
// separate, fully-isolated history collection (e.g. "Main" -> "Main_prev").
// History lives in its own collection so live tag/path queries never scan it
// and the live association index stays lean.
const prevCollectionSuffix = "_prev"

// prevCollectionFor returns the history-collection name for a main collection.
// An empty mainCollection resolves to the configured default first.
func (tc *TieClient) prevCollectionFor(mainCollection string) string {
	if mainCollection == "" {
		mainCollection = tc.Config.Collection
	}
	return mainCollection + prevCollectionSuffix
}

// versionLocKey is the opaque key grouping every superseded version of one
// logical file: the directory it lived in (a main-collection DirUID) plus its
// filename. In the prev collection this is just a string key, not an entity, so
// history needs no path tree / root dir / DirUIDs of its own.
func versionLocKey(parentUID DirUID, name string) string {
	return string(parentUID) + "\x00" + name
}

// VersionInfo describes one superseded version of a file, read from the history
// collection. Date is the supersession time (when this content was replaced).
type VersionInfo struct {
	Hash     string
	Filename string
	Size     int
	TieType  TieType
	Date     time.Time
}

// supersedeToPrev records oldHash (the content currently at parentUID/name in
// the main collection) as a version in the history collection and removes it
// from main. It is the single path both WriteFile and reconcileDir use to
// version a superseded file.
//
// Ordering is deliberate for durability: the version is written and synced to
// the prev collection BEFORE main's live pointer is mutated, so a crash between
// the two steps leaves the old content still findable (as a duplicate in main
// or as a version in prev) — never lost.
//
// When PrevVersions <= 0 no history is kept: the old edge is dropped outright
// (and the content's metadata garbage-collected if it was the last reference),
// matching the pre-existing no-history behavior.
func supersedeToPrev(tc *TieClient, mainCollection string, parentUID DirUID, name, oldHash string) error {
	if tc.Config.PrevVersions <= 0 {
		return detachChild(tc, mainCollection, oldHash, parentUID)
	}

	oldRow, err := tc.GetIn(mainCollection, oldHash)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	prevCol := tc.prevCollectionFor(mainCollection)
	locKey := versionLocKey(parentUID, name)

	// 1. Record the version in prev first. Replicate every descriptive triple
	//    except parent — placement in history is expressed by version-of, not a
	//    parent edge.
	b := tc.NewBatchIn(prevCol)
	for relation, values := range oldRow.Attributes {
		if relation == str(TieParent) {
			continue
		}
		for _, v := range values {
			b.Add(oldHash, relation, v)
		}
	}
	b.Add(oldHash, str(TieVersionOf), locKey)
	b.Set(oldHash, str(TieVersionDate), []string{time.Now().Format(tagDateFormat)})
	if _, err := tc.Batch(b); err != nil {
		return err
	}
	if err := tc.SyncIn(prevCol); err != nil {
		return err
	}

	// 2. Remove the old content from main (its edge, and its metadata if this
	//    was its last parent — shared content stays live elsewhere).
	if err := detachChild(tc, mainCollection, oldHash, parentUID); err != nil {
		return err
	}

	// 3. Retention: keep at most PrevVersions per logical file.
	return trimVersions(tc, prevCol, locKey, tc.Config.PrevVersions)
}

// listVersionRecords returns the version records for one logical file as
// childEntry values (Hash + Filename + supersession date), for retention
// ordering via retentionDrops.
func listVersionRecords(tc *TieClient, prevCol, locKey string) ([]childEntry, error) {
	rows, _, err := tc.QueryIn(prevCol, QuerySpec{
		Terms:   []string{locKey},
		Filter:  str(TieVersionOf),
		Reverse: true,
		Expand:  true,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]childEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, childEntry{
			Hash:     row.Key,
			Filename: RowFirst(row, str(TieFilename)),
			TagDate:  parseTagDate(RowFirst(row, str(TieVersionDate))),
		})
	}
	return out, nil
}

// trimVersions drops the oldest version records for locKey so at most keep
// remain (oldest-first, matching retentionDrops).
func trimVersions(tc *TieClient, prevCol, locKey string, keep int) error {
	versions, err := listVersionRecords(tc, prevCol, locKey)
	if err != nil {
		return err
	}
	for _, d := range retentionDrops(versions, keep) {
		if err := dropVersion(tc, prevCol, d.Hash, locKey); err != nil {
			return err
		}
	}
	return nil
}

// dropVersion removes hash's version record for one logical location (locKey)
// from the prev collection. If hash is a version of no other location, its
// replicated metadata (and version-date) are removed too; otherwise only this
// location's version-of edge is dropped so shared-content records elsewhere
// stay intact. This mirrors detachChild's shared-content guard, for version-of.
func dropVersion(tc *TieClient, prevCol, hash, locKey string) error {
	edge := tc.NewBatchIn(prevCol)
	edge.Delete(hash, str(TieVersionOf), locKey)
	if _, err := tc.Batch(edge); err != nil {
		return err
	}
	if err := tc.SyncIn(prevCol); err != nil {
		return err
	}

	row, err := tc.GetIn(prevCol, hash)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(RowValues(row, str(TieVersionOf))) > 0 {
		return nil // still a version of another location; keep its metadata
	}
	meta := tc.NewBatchIn(prevCol)
	for relation, values := range row.Attributes {
		for _, v := range values {
			meta.Delete(hash, relation, v)
		}
	}
	if _, err := tc.Batch(meta); err != nil {
		return err
	}
	return tc.SyncIn(prevCol)
}

// resolveFileLoc splits a virtual file path (e.g. "/docs/note.txt" or
// "tie:/docs/note.txt") into the parent directory's DirUID (resolved in the
// main collection) and the file's basename.
func (tc *TieClient) resolveFileLoc(path string) (DirUID, string, error) {
	clean := strings.TrimPrefix(path, FileURIScheme)
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	name := filepath.Base(clean)
	dir := filepath.Dir(clean)
	parentUID, err := tc.DirUIDFromPath(dir)
	if err != nil {
		return "", "", err
	}
	if parentUID == "" {
		return "", "", fmt.Errorf("directory not found: %s", dir)
	}
	return parentUID, name, nil
}

// ListVersions returns the superseded versions of the file at path, newest
// first. An empty mainCollection uses the configured default. A file with no
// history yields an empty slice, not an error.
func (tc *TieClient) ListVersions(mainCollection, path string) ([]VersionInfo, error) {
	parentUID, name, err := tc.resolveFileLoc(path)
	if err != nil {
		return nil, err
	}
	prevCol := tc.prevCollectionFor(mainCollection)
	rows, _, err := tc.QueryIn(prevCol, QuerySpec{
		Terms:   []string{versionLocKey(parentUID, name)},
		Filter:  str(TieVersionOf),
		Reverse: true,
		Expand:  true,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, row := range rows {
		size, _ := strconv.Atoi(RowFirst(row, str(TieFilesize)))
		out = append(out, VersionInfo{
			Hash:     row.Key,
			Filename: RowFirst(row, str(TieFilename)),
			Size:     size,
			TieType:  StringToTieType(RowFirst(row, str(TieTypeProperty))),
			Date:     parseTagDate(RowFirst(row, str(TieVersionDate))),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.After(out[j].Date)
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// RestoreVersion makes a superseded version of path the live content again. If
// versionHash is empty the newest version is restored; otherwise the version
// whose hash equals or is prefixed by versionHash is used. The currently-live
// content (if any) is first recorded as a version (prev-first ordering), then
// the target is installed live in main from its history snapshot, and finally
// the target's now-redundant history record is removed. The target's blob
// already exists on the filehost, so no upload happens. Returns the restored
// content hash.
func (tc *TieClient) RestoreVersion(mainCollection, path, versionHash string) (string, error) {
	parentUID, name, err := tc.resolveFileLoc(path)
	if err != nil {
		return "", err
	}
	prevCol := tc.prevCollectionFor(mainCollection)
	locKey := versionLocKey(parentUID, name)

	versions, err := tc.ListVersions(mainCollection, path)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no versions recorded for %s", path)
	}
	target := versions[0].Hash
	if versionHash != "" {
		target = ""
		for _, v := range versions {
			if v.Hash == versionHash || strings.HasPrefix(v.Hash, versionHash) {
				target = v.Hash
				break
			}
		}
		if target == "" {
			return "", fmt.Errorf("no version %q for %s", versionHash, path)
		}
	}

	children, err := fileChildren(tc, parentUID)
	if err != nil {
		return "", err
	}
	current := ""
	for _, c := range children {
		if c.Filename == name {
			current = c.Hash
			break
		}
	}
	if current == target {
		return target, nil // already live
	}

	if current != "" {
		if err := supersedeToPrev(tc, mainCollection, parentUID, name, current); err != nil {
			return "", err
		}
	}

	prevRow, err := tc.GetIn(prevCol, target)
	if err != nil {
		return "", err
	}
	b := tc.NewBatchIn(mainCollection)
	for relation, values := range prevRow.Attributes {
		if relation == str(TieVersionOf) || relation == str(TieVersionDate) {
			continue
		}
		for _, v := range values {
			b.Add(target, relation, v)
			if relation == str(TieTag) {
				b.Add(str(TieTags), str(TieAll), v) // keep the tag registry consistent
			}
		}
	}
	b.Add(target, str(TieParent), str(parentUID))
	if _, err := tc.Batch(b); err != nil {
		return "", err
	}
	if err := tc.SyncIn(mainCollection); err != nil {
		return "", err
	}

	// The target is live again, so it is no longer a version of this location.
	if err := dropVersion(tc, prevCol, target, locKey); err != nil {
		return "", err
	}
	return target, nil
}
