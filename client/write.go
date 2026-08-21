package client

import (
	"errors"
	"fmt"
	"io"
	"os"

	"git.sr.ht/~uid/tie/io/putlib"
)

// WriteFile uploads srcPath's bytes to host and places the content as name
// under directory parent, writing the standard file triples (filename, name,
// media-type, tie-type, filesize, tag-date, parent). It is the single write
// path shared by the FUSE mount's create/edit-save and by direct client
// callers (e.g. tie-fm copy-in).
//
// If parent already holds a file named name with different content, the
// superseded version is recorded in the isolated "<Collection>_prev" history
// collection, keeping at most Config.PrevVersions of it (oldest dropped first) —
// the same reconciliation ImportDir performs, but scoped to this one file so
// other children of parent are never disturbed. When PrevVersions is 0 the old
// edge is removed outright (and the content garbage-collected if unreferenced).
//
// The new content inherits the superseded version's tags and all other
// descriptive attributes (title/artist/album/year/track/…); only the fields
// recomputed from the new bytes (filename/name/filesize/tag-date/media-type/
// tie-type) and the parent edge are set fresh. An unchanged re-save (identical
// bytes → identical hash) is a no-op. Returns the new content hash.
func (tc *TieClient) WriteFile(host FileHost, collection string, parent DirUID, name, srcPath string, extraTags []string) (string, error) {
	return tc.WriteFileWithProgress(host, collection, parent, name, srcPath, extraTags, nil)
}

// WriteFileWithProgress behaves exactly like WriteFile but, when progress is
// non-nil, writes each chunk of uploaded bytes to it so callers can render an
// upload progress bar. The total byte count equals srcPath's size.
func (tc *TieClient) WriteFileWithProgress(host FileHost, collection string, parent DirUID, name, srcPath string, extraTags []string, progress io.Writer) (string, error) {
	stat, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}

	status := putlib.Upload(host.URL, srcPath, putlib.PutConfig{Client: HTTPClientFor(host), Progress: progress})
	if status.ErrorMsg != "" {
		return "", fmt.Errorf("upload failed for %q: %s", name, status.ErrorMsg)
	}
	newHash := status.LastItem.Hash

	// Find an existing same-named file child of parent. fileChildren (not
	// ReadTieDir) is used deliberately: it does not filter by media tie-type, so
	// a superseded plain file is visible — matching how reconcileDir keys.
	children, err := fileChildren(tc, parent)
	if err != nil {
		return "", err
	}
	oldHash, hasOld := "", false
	for _, c := range children {
		if c.Filename == name {
			oldHash, hasOld = c.Hash, true
			break
		}
	}

	// Unchanged content: no triple churn, no spurious version.
	if hasOld && oldHash == newHash {
		return newHash, nil
	}

	fileType, err := GetTieTypeFromPath(srcPath)
	if err != nil {
		fileType = TieFile
	}

	batch := tc.NewBatchIn(collection)
	appendTagOps(batch, TagInfo{
		Hash:      newHash,
		File:      name,
		Size:      int(stat.Size()),
		MediaType: status.LastItem.MediaType,
		TieType:   fileType,
		Tags:      extraTags,
		Directory: parent,
	})

	// Carry the superseded version's descriptive attributes onto the new hash,
	// skipping the fields recomputed above (and parent, written by appendTagOps).
	if hasOld {
		recomputed := map[string]bool{
			str(TieFilename):     true,
			str(TieName):         true,
			str(TieFilesize):     true,
			str(TieTagDate):      true,
			str(TieMediaType):    true,
			str(TieTypeProperty): true,
			str(TieParent):       true,
		}
		oldRow, err := tc.Get(oldHash)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
		for relation, values := range oldRow.Attributes {
			if recomputed[relation] {
				continue
			}
			for _, v := range values {
				batch.Add(newHash, relation, v)
				if relation == str(TieTag) {
					batch.Add(str(TieTags), str(TieAll), v) // keep the tag registry consistent
				}
			}
		}
	}

	if _, err := tc.Batch(batch); err != nil {
		return "", err
	}
	if err := tc.Sync(); err != nil {
		return "", err
	}

	if !hasOld {
		return newHash, nil
	}

	// Version (or, at PrevVersions<=0, drop) the superseded content into the
	// isolated history collection. Same helper reconcileDir uses.
	if err := supersedeToPrev(tc, collection, parent, name, oldHash); err != nil {
		return "", err
	}
	return newHash, nil
}
