package client

// verify.go implements a filesystem-consistency check ("fsck") for a
// collection's virtual file tree. The tree is a set of DirUID nodes
// (directories) and content-hash nodes (files) joined by (child, "parent",
// parentUID) edges; a node that loses every parent edge becomes unreachable
// from the root and vanishes from every path query and FUSE mount — the failure
// mode the 98fe9db read-before-write bug could produce by silently dropping
// triples during reconciliation.
//
// Verify walks the whole tree server-side universe (via reverse tie-type
// queries, the same pattern UntaggedFiles uses) and reports structural and
// metadata problems. It is read-only; RepairOrphans is the separate, explicit
// mutating step that re-homes orphaned nodes under a restored/ directory.

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// VerifyReport is the result of a Verify pass. Each slice holds the subjects
// (DirUIDs or content hashes) that failed one check; a healthy store returns
// all of them empty. The report is meant to be both printed for a human and
// consumed programmatically (e.g. by RepairOrphans).
type VerifyReport struct {
	// OrphanDirs are directories with no parent edge (unreachable from root).
	OrphanDirs []DirUID
	// OrphanFiles are file content-hashes with no parent edge.
	OrphanFiles []string
	// DanglingParentRefs are (child, parentUID) edges whose parentUID no longer
	// exists as a directory — a half-deleted link.
	DanglingParentRefs []ParentRef
	// Cycles are parent-edge loops (a directory that is its own ancestor); each
	// entry is the loop's UID chain. Reported, never auto-repaired.
	Cycles [][]DirUID
	// DuplicatePaths maps a virtual path to the >1 DirUIDs that all claim it.
	// Reported, never auto-repaired (merging UIDs is a destructive decision).
	DuplicatePaths map[string][]DirUID
	// MissingMetadata are files lacking one of the core triples appendTagOps
	// always writes (filename, filesize, media-type, tie-type), or directories
	// lacking a name — a sign of an incomplete import.
	MissingMetadata []MetadataGap
	// MissingBlobs are file hashes whose content is absent from the filehost
	// (never uploaded, or reaped). Only populated when checkBlobs is set.
	MissingBlobs []string
	// Counts of the universes scanned, for the summary line.
	DirCount  int
	FileCount int
}

// ParentRef is one (child, parent) edge, used to report a dangling reference.
type ParentRef struct {
	Child  string // DirUID or content hash carrying the edge
	Parent string // the referenced DirUID that does not exist
}

// MetadataGap is one node and the list of required relations it lacks.
type MetadataGap struct {
	Subject string // DirUID or content hash
	IsDir   bool
	Missing []string // relation names, e.g. "filename"
}

// Problems reports whether any check failed (i.e. the store is not clean).
func (r *VerifyReport) Problems() int {
	n := len(r.OrphanDirs) + len(r.OrphanFiles) + len(r.DanglingParentRefs) +
		len(r.Cycles) + len(r.DuplicatePaths) + len(r.MissingMetadata) + len(r.MissingBlobs)
	return n
}

// rootPathValue is the virtual path of the tree root, whose parent is itself.
const rootPathValue = FileURIScheme + "/"

// Verify scans collection's virtual file tree and reports structural and
// metadata inconsistencies. An empty collection falls back to the configured
// default. When checkBlobs is true, every file's content hash is also stat'ed
// on each configured default filehost (HEAD request) and reported if absent —
// the slow part of the pass, so it is opt-in.
//
// The pass is read-only. It fetches the full directory and file universes
// (unpaginated) and expands each node's forward attributes; memory is O(tree
// size), consistent with the rest of the client.
func (tie *TieClient) Verify(collection string, checkBlobs bool) (*VerifyReport, error) {
	rep := &VerifyReport{DuplicatePaths: make(map[string][]DirUID)}

	// The structural tie-type "directory" marker is written on two different
	// kinds of node: a live path-tree DirUID (which also carries a "path"
	// triple), and an immutable tiedir snapshot blob (a file-addressed manifest,
	// media-type inode/directory) that ImportDir links from its live node via a
	// "tiedir-hash" edge. Only the former is a directory for tree purposes; the
	// latter is structurally a file (it has no children and needs a parent like
	// any other content). Partition the universe on the presence of a path
	// triple so tiedir blobs are verified as files, not mistaken for orphaned
	// directories.
	dirRows, err := tie.allOfType(collection, str(TieDirectory))
	if err != nil {
		return nil, err
	}
	fileRows, err := tie.allOfType(collection, str(TieFile))
	if err != nil {
		return nil, err
	}
	var dirs []Row
	files := fileRows
	for _, row := range dirRows {
		if len(RowValues(row, str(TiePath))) > 0 {
			dirs = append(dirs, row)
		} else {
			files = append(files, row) // tiedir blob: a directory's content, not a node
		}
	}
	rep.DirCount = len(dirs)
	rep.FileCount = len(files)

	// Index directories by UID for parent/cycle checks, and group by path for
	// the duplicate-path check. The root (path == "tie:/") is exempt from the
	// orphan check because its parent is itself.
	dirByUID := make(map[string]Row, len(dirs))
	pathClaim := make(map[string][]DirUID)
	for _, row := range dirs {
		dirByUID[row.Key] = row
		for _, p := range RowValues(row, str(TiePath)) {
			pathClaim[p] = append(pathClaim[p], DirUID(row.Key))
		}
	}
	for p, uids := range pathClaim {
		if len(uids) > 1 {
			sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
			rep.DuplicatePaths[p] = uids
		}
	}

	// Structural checks on directories.
	for _, row := range dirs {
		uid := row.Key
		parents := RowValues(row, str(TieParent))
		isRoot := RowHas(row, str(TiePath), rootPathValue)

		if !isRoot && len(parents) == 0 {
			rep.OrphanDirs = append(rep.OrphanDirs, DirUID(uid))
		}
		for _, p := range parents {
			if p == uid {
				continue // the root's self-edge; also a 1-node cycle, but benign
			}
			if _, ok := dirByUID[p]; !ok {
				rep.DanglingParentRefs = append(rep.DanglingParentRefs, ParentRef{Child: uid, Parent: p})
			}
		}
		rep.checkDirMetadata(row)
	}
	rep.Cycles = detectCycles(dirByUID)

	// Structural + metadata checks on files.
	for _, row := range files {
		parents := RowValues(row, str(TieParent))
		if len(parents) == 0 {
			rep.OrphanFiles = append(rep.OrphanFiles, row.Key)
		}
		for _, p := range parents {
			if _, ok := dirByUID[p]; !ok {
				rep.DanglingParentRefs = append(rep.DanglingParentRefs, ParentRef{Child: row.Key, Parent: p})
			}
		}
		rep.checkFileMetadata(row)
	}

	// Sort for deterministic output.
	sort.Slice(rep.OrphanDirs, func(i, j int) bool { return rep.OrphanDirs[i] < rep.OrphanDirs[j] })
	sort.Strings(rep.OrphanFiles)
	sort.Slice(rep.DanglingParentRefs, func(i, j int) bool {
		if rep.DanglingParentRefs[i].Child != rep.DanglingParentRefs[j].Child {
			return rep.DanglingParentRefs[i].Child < rep.DanglingParentRefs[j].Child
		}
		return rep.DanglingParentRefs[i].Parent < rep.DanglingParentRefs[j].Parent
	})
	sort.Slice(rep.MissingMetadata, func(i, j int) bool { return rep.MissingMetadata[i].Subject < rep.MissingMetadata[j].Subject })

	if checkBlobs {
		missing, err := tie.checkBlobsExist(files)
		if err != nil {
			return rep, err // return the partial structural report plus the error
		}
		rep.MissingBlobs = missing
	}

	return rep, nil
}

// allOfType returns every Row carrying the structural tie-type value (e.g.
// "directory", "file"), unpaginated, via a reverse tie-type query with
// Expand. ErrNotFound maps to an empty set (no such items yet).
func (tie *TieClient) allOfType(collection, typeValue string) ([]Row, error) {
	rows, _, err := tie.QueryIn(collection, QuerySpec{
		Terms:   []string{typeValue},
		Filter:  str(TieTypeProperty),
		Reverse: true,
		Expand:  true,
		Limit:   -1, // no limit: verify must see the whole universe
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// checkDirMetadata records a gap for each required directory relation a row
// lacks. Structural triples (path/tie-type/parent) are checked elsewhere. A
// directory legitimately has no descriptive filename when it is an intermediate
// path node created only by MkTieDirAll (tie:/tmp, tie:/, ...) — only leaf
// directories get a display name from tagDir — so filename is NOT required.
// There is currently no descriptive relation every directory must hold, so this
// is a no-op placeholder kept for symmetry and future dir-metadata rules.
func (rep *VerifyReport) checkDirMetadata(row Row) {
}

// checkFileMetadata records a gap for each required file relation a row lacks.
// appendTagOps always writes filename, filesize and media-type, so a missing
// one marks an incomplete import. A tiedir blob (media-type inode/directory) is
// exempt from the media-type requirement: it is a directory's content snapshot,
// and older tag paths label it with only the structural "directory" marker, so
// it may carry no media-type or concrete tie-type. The tie-type check is
// likewise only applied to real files, which should carry a concrete media
// tie-type beyond the structural "file" marker.
func (rep *VerifyReport) checkFileMetadata(row Row) {
	isTiedir := RowHas(row, str(TieTypeProperty), str(TieDirectory)) ||
		RowFirst(row, str(TieMediaType)) == "inode/directory"
	var missing []string
	required := []TieProperty{TieFilename, TieFilesize}
	if !isTiedir {
		required = append(required, TieMediaType)
	}
	for _, rel := range required {
		if len(RowValues(row, str(rel))) == 0 {
			missing = append(missing, str(rel))
		}
	}
	if !isTiedir {
		// A real file should carry a concrete media tie-type beyond the
		// structural "file" marker (image-file, audio-file, ...); only the
		// marker means the type was never classified.
		types := RowValues(row, str(TieTypeProperty))
		if len(types) == 0 || (len(types) == 1 && types[0] == str(TieFile)) {
			missing = append(missing, str(TieTypeProperty))
		}
	}
	if len(missing) > 0 {
		rep.MissingMetadata = append(rep.MissingMetadata, MetadataGap{Subject: row.Key, IsDir: false, Missing: missing})
	}
}

// detectCycles finds parent-edge loops among directories. It walks each
// directory's parent chain; a chain that revisits a node without reaching the
// root is a cycle. The root's self-edge is excluded. Each cycle is reported
// once as its sorted UID set.
func detectCycles(dirByUID map[string]Row) [][]DirUID {
	parents := make(map[string][]string, len(dirByUID))
	for uid, row := range dirByUID {
		for _, p := range RowValues(row, str(TieParent)) {
			if p != uid { // skip the root self-edge
				parents[uid] = append(parents[uid], p)
			}
		}
	}
	seen := make(map[string]bool)    // nodes already confirmed cycle-free or reported
	inCycle := make(map[string]bool) // nodes belonging to a reported cycle
	var cycles [][]DirUID
	reported := make(map[string]bool) // dedup by canonical cycle key

	var walk func(uid string, trail []string)
	walk = func(uid string, trail []string) {
		if inCycle[uid] || seen[uid] {
			return
		}
		for _, t := range trail {
			if t == uid {
				// Found a cycle: the sub-chain from the first occurrence of uid.
				start := 0
				for i, x := range trail {
					if x == uid {
						start = i
						break
					}
				}
				cyc := append([]string{}, trail[start:]...)
				sort.Strings(cyc)
				key := fmt.Sprint(cyc)
				if !reported[key] {
					reported[key] = true
					out := make([]DirUID, len(cyc))
					for i, u := range cyc {
						out[i] = DirUID(u)
						inCycle[u] = true
					}
					cycles = append(cycles, out)
				}
				return
			}
		}
		trail = append(trail, uid)
		ps, ok := parents[uid]
		if !ok || len(ps) == 0 {
			seen[uid] = true // reached a root or an orphan; no cycle through here
			return
		}
		for _, p := range ps {
			walk(p, trail)
		}
		seen[uid] = true
	}
	for uid := range parents {
		walk(uid, nil)
	}
	sort.Slice(cycles, func(i, j int) bool { return fmt.Sprint(cycles[i]) < fmt.Sprint(cycles[j]) })
	return cycles
}

// checkBlobsExist stats every file's content hash on each configured default
// filehost and returns the hashes absent from all of them. One HEAD request per
// hash per host; concurrency is kept modest to avoid hammering the server.
func (tie *TieClient) checkBlobsExist(files []Row) ([]string, error) {
	hosts := tie.Config.DefaultFileHosts
	if len(hosts) == 0 {
		return nil, errors.New("no DefaultFileHosts configured for blob check")
	}
	// Use the first configured host for existence; content-addressed blobs are
	// identical across hosts, so presence on any one is what matters for
	// reachability of the canonical copy.
	host, ok := tie.Config.FileHosts[hosts[0]]
	if !ok {
		return nil, fmt.Errorf("default filehost %q has no [FileHosts] entry", hosts[0])
	}
	hc := HTTPClientFor(host)

	var missing []string
	for _, row := range files {
		hash := row.Key
		if len(hash) != 64 {
			continue // not a content hash (shouldn't happen for a file); skip
		}
		ok, err := blobExists(hc, host.URL, hash)
		if err != nil {
			return missing, err
		}
		if !ok {
			missing = append(missing, hash)
		}
	}
	return missing, nil
}

// blobExists issues a HEAD request for hash against the filehost and reports
// whether the blob is present (200) or absent (404). Any other status or a
// transport error is surfaced as an error rather than misreported as absence.
func blobExists(hc *http.Client, baseURL, hash string) (bool, error) {
	req, err := http.NewRequest(http.MethodHead, baseURL+"/"+hash, nil)
	if err != nil {
		return false, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("filehost stat %s: unexpected status %s", hash, resp.Status)
	}
}

// RepairOrphans re-homes every orphan in rep under a timestamped restored/
// directory, making them reachable from the tree root again. It is the explicit,
// mutating counterpart to Verify and is only ever called at the user's request
// (`tie verify --repair`). Only orphans are repaired — duplicate paths, cycles,
// dangling refs and missing metadata are reported but left for a human decision.
//
// Each orphan is re-parented under tie:/restored/<date>/ (directories and files
// share the one recovery dir); its own metadata is left untouched. The restore
// directory is created via MkTieDirAll (so it also gets its structural triples
// and is itself browsable).
//
// rep should come from a just-run Verify on the same collection; stale reports
// are harmless (re-parenting an already-parented node just adds another parent
// edge), but a fresh pass avoids re-homing nodes another client already fixed.
func (tie *TieClient) RepairOrphans(collection string, rep *VerifyReport, destDir string) (int, error) {
	if rep == nil || (len(rep.OrphanDirs) == 0 && len(rep.OrphanFiles) == 0) {
		return 0, nil
	}
	if destDir == "" {
		destDir = FileURIScheme + "/restored/" + time.Now().Format("2006-01-02")
	}
	if !strings.HasPrefix(destDir, FileURIScheme) {
		destDir = FileURIScheme + "/" + destDir
	}
	parentUID, err := tie.MkTieDirAll(destDir)
	if err != nil {
		return 0, err
	}

	batch := tie.NewBatchIn(collection)
	n := 0
	for _, uid := range rep.OrphanDirs {
		batch.Add(string(uid), str(TieParent), string(parentUID))
		n++
	}
	for _, hash := range rep.OrphanFiles {
		batch.Add(hash, str(TieParent), string(parentUID))
		n++
	}
	if _, err := tie.Batch(batch); err != nil {
		return 0, err
	}
	if err := tie.SyncIn(collection); err != nil {
		return 0, err
	}
	return n, nil
}
