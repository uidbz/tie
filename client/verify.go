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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	// PathlessDirs are dir-typed nodes with no path triple (#118): real
	// directories that lost their path, so no path-based view (tie-view's tree,
	// tie-fm, the FUSE mount) can reach them. They are not content — their key
	// is a DirUID — so they are never blob-checked.
	PathlessDirs []PathlessDir
	// PathMismatch are directories whose path triple claims a location that
	// none of their parents is at (#119). Such a node is missing from the
	// branch its own path names and shows up, mislabelled, wherever a parent
	// happens to list it.
	PathMismatch []PathMismatch
	// MissingTieType are nodes that carry a path triple — which only a directory
	// does — but have lost the structural `tie-type` marker, so neither of
	// verify's tie-type universes contains them and they escape every other
	// check (#122). The marker is re-derivable from the path.
	MissingTieType []MarkerGap
	// Counts of the universes scanned, for the summary line.
	DirCount  int
	FileCount int
}

// PathlessDir is one dir-typed node without a path triple, with what a repair
// would restore it to.
type PathlessDir struct {
	UID  DirUID
	Name string // its recorded filename/name triple, if any
	Path string // <live parent path>/<Name>, or "" when not derivable
}

// PathMismatch is one directory whose location and path disagree.
type PathMismatch struct {
	UID        DirUID
	Path       string // the location the node claims
	ParentPath string // where a parent actually lists it ("" when unknowable)
}

// MarkerGap is one node that carries a path but no structural tie-type, with
// the path that identifies what it is.
type MarkerGap struct {
	UID  DirUID
	Path string
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
		len(r.Cycles) + len(r.DuplicatePaths) + len(r.MissingMetadata) + len(r.MissingBlobs) +
		len(r.PathlessDirs) + len(r.PathMismatch) + len(r.MissingTieType)
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
// Verify reads the whole tree of the client's scoped collection and reports what
// is wrong with it. checkBlobs additionally stats every content hash on the
// collection's filehost.
func (tie *TieClient) Verify(checkBlobs bool) (*VerifyReport, error) {
	rep := &VerifyReport{DuplicatePaths: make(map[string][]DirUID)}

	// The structural tie-type "directory" marker is written on two different
	// kinds of node: a live path-tree DirUID, and an immutable tiedir snapshot
	// blob (a file-addressed manifest) that ImportDir links from its live node
	// via a "tiedir-hash" edge. Partition on that relation, not on whether a
	// node still carries a path triple: keying the split on the path mistook a
	// live directory that had *lost* its path for a content blob, which made
	// --check-blobs report a DirUID as a missing blob and hid the real defect
	// (#118). A dir-typed node nobody references via tiedir-hash is a tree node
	// — a broken one when it has no path — and its key is never content.
	dirRows, err := tie.allOfType(str(TieDirectory))
	if err != nil {
		return nil, err
	}
	fileRows, err := tie.allOfType(str(TieFile))
	if err != nil {
		return nil, err
	}
	var dirs []Row
	files := fileRows
	for _, row := range dirRows {
		switch {
		case len(RowValues(row, RelPath)) > 0:
			dirs = append(dirs, row)
		case tie.referencedByTiedir(row.Key):
			files = append(files, row) // a tiedir snapshot blob: a directory's content, not a node
		default:
			dirs = append(dirs, row)
			rep.PathlessDirs = append(rep.PathlessDirs, PathlessDir{
				UID:  DirUID(row.Key),
				Name: firstOf(RowFirst(row, RelFilename), RowFirst(row, RelName)),
			})
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
		for _, p := range RowValues(row, RelPath) {
			pathClaim[p] = append(pathClaim[p], DirUID(row.Key))
		}
	}

	// A parent value the tie-type universe does not know may still be a
	// directory. `path` triples are written only on directory nodes — a content
	// node's position is its parent edge alone — so a node carrying one that has
	// lost its structural tie-type marker is a directory whose children would
	// otherwise be reported as dangling references, and a fixing repair would
	// move those children up a level, away from the parent standing right there
	// (#122). Look each unknown parent up once and bring the path-carrying ones
	// into the tree, recording that the marker has to be put back.
	var unknown []string
	seen := map[string]bool{}
	for _, set := range [][]Row{dirs, files} {
		for _, row := range set {
			for _, p := range RowValues(row, RelParent) {
				if _, known := dirByUID[p]; known || p == row.Key || seen[p] {
					continue
				}
				seen[p] = true
				unknown = append(unknown, p)
			}
		}
	}
	sort.Strings(unknown)
	for _, p := range unknown {
		row, err := tie.Get(p)
		if err != nil || len(RowValues(row, RelPath)) == 0 {
			continue // no such node, or nothing a path says: genuinely dangling
		}
		dirByUID[p] = row
		ps := RowValues(row, RelPath)
		for _, pp := range ps {
			pathClaim[pp] = append(pathClaim[pp], DirUID(p))
		}
		rep.MissingTieType = append(rep.MissingTieType, MarkerGap{UID: DirUID(p), Path: ps[0]})
	}

	// The scan above is edge-driven: it finds a marker gap because something
	// points at the node. A childless directory that lost its marker, and whose
	// own parent edge is gone too, is referenced by nothing and so turns up
	// nowhere — it stays browsable while escaping the orphan, metadata and cycle
	// checks. The store can answer the question directly: every subject carrying a
	// `path` triple, whatever its value (#124). Only directory nodes carry a path,
	// so anything here the tree does not already know is such a node.
	pathHolders, err := tie.allHoldingRelation(string(RelPath))
	if err != nil {
		return nil, err
	}
	for _, row := range pathHolders {
		if _, known := dirByUID[row.Key]; known {
			continue // already a known directory, or found through an edge above
		}
		dirByUID[row.Key] = row
		for _, pp := range RowValues(row, RelPath) {
			pathClaim[pp] = append(pathClaim[pp], DirUID(row.Key))
		}
		rep.MissingTieType = append(rep.MissingTieType, MarkerGap{UID: DirUID(row.Key), Path: RowFirst(row, RelPath)})
	}

	for p, uids := range pathClaim {
		if len(uids) > 1 {
			sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
			rep.DuplicatePaths[p] = uids
		}
	}

	// A path triple is only meaningful if a parent of the node lives at the
	// location that path names. Every path-based view walks that relation twice,
	// from opposite ends — ReadTieDir lists children by the reverse parent query
	// and names them by their path, DirUIDFromPath resolves that name back to a
	// node — so a node whose parents sit elsewhere is missing from the branch
	// its own path claims (#119). Duplicate claims were checked; this was not.
	for _, row := range dirs {
		ps := RowValues(row, RelPath)
		if len(ps) == 0 || RowHas(row, RelPath, rootPathValue) {
			continue
		}
		parents := RowValues(row, RelParent)
		if len(parents) == 0 {
			continue // already reported as an orphan
		}
		var actual []string
		consistent := false
		for _, p := range parents {
			if p == row.Key {
				continue // the root's self-edge
			}
			prow, ok := dirByUID[p]
			if !ok {
				continue // dangling reference, reported separately
			}
			for _, pp := range RowValues(prow, RelPath) {
				actual = append(actual, pp)
				if pathUnder(ps[0], pp) {
					consistent = true
				}
			}
		}
		if consistent || len(actual) == 0 {
			continue // agrees, or no parent has a path to compare against
		}
		sort.Strings(actual)
		rep.PathMismatch = append(rep.PathMismatch, PathMismatch{
			UID: DirUID(row.Key), Path: ps[0], ParentPath: actual[0],
		})
	}

	// Work out what a path-less directory should be called where it lives, so
	// the finding carries the repair and a human sees which folder it was.
	for i := range rep.PathlessDirs {
		pd := &rep.PathlessDirs[i]
		if pd.Name == "" {
			continue
		}
		var cands []string
		for _, p := range RowValues(dirByUID[string(pd.UID)], RelParent) {
			prow, ok := dirByUID[p]
			if !ok {
				continue
			}
			for _, pp := range RowValues(prow, RelPath) {
				cands = append(cands, joinVirtualPath(pp, pd.Name))
			}
		}
		sort.Strings(cands)
		if len(cands) > 0 {
			pd.Path = cands[0]
		}
	}

	// Structural checks on directories.
	for _, row := range dirs {
		uid := row.Key
		parents := RowValues(row, RelParent)
		isRoot := RowHas(row, RelPath, rootPathValue)

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
		parents := RowValues(row, RelParent)
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
	sort.Slice(rep.PathlessDirs, func(i, j int) bool { return rep.PathlessDirs[i].UID < rep.PathlessDirs[j].UID })
	sort.Slice(rep.PathMismatch, func(i, j int) bool {
		if rep.PathMismatch[i].UID != rep.PathMismatch[j].UID {
			return rep.PathMismatch[i].UID < rep.PathMismatch[j].UID
		}
		return rep.PathMismatch[i].Path < rep.PathMismatch[j].Path
	})
	sort.Slice(rep.MissingTieType, func(i, j int) bool { return rep.MissingTieType[i].UID < rep.MissingTieType[j].UID })

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
// Expand. ErrNotFound maps to an empty set (no such items yet). It reads the
// client's scoped collection.
func (tie *TieClient) allOfType(typeValue string) ([]Row, error) {
	rows, _, err := tie.Query(QuerySpec{
		Terms:   []string{typeValue},
		Filter:  string(RelType),
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

// allHoldingRelation returns every Row carrying at least one triple under
// relation, whatever its value, unpaginated and with its full attributes. It is
// the client shape of the store's relation-presence predicate (#124): the
// question neither query direction can ask from a term, because both key on a
// value. ErrNotFound maps to an empty set.
func (tie *TieClient) allHoldingRelation(relation string) ([]Row, error) {
	rows, _, err := tie.Query(QuerySpec{
		HasRelation: relation,
		Filter:      relation,
		Expand:      true,
		Limit:       -1, // no limit: verify must see the whole universe
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// referencedByTiedir reports whether any node references uid through a
// tiedir-hash edge — which makes uid that directory's immutable content
// snapshot rather than a node in the path tree. It is the structural test both
// Verify and Repair need: shape (tie-type, or the presence of a path) cannot
// tell a snapshot blob from a live directory that lost its path.
func (tie *TieClient) referencedByTiedir(uid string) bool {
	rows, _, err := tie.Query(QuerySpec{
		Terms: []string{uid}, Filter: string(RelTiedirHash), Reverse: true, Limit: -1,
	})
	return err == nil && len(rows) > 0
}

// pathUnder reports whether child sits inside the virtual directory parentPath
// ("tie:/a/b" is under "tie:/a", not under "tie:/a/b" itself).
func pathUnder(child, parentPath string) bool {
	if child == "" || parentPath == "" || child == parentPath {
		return false
	}
	prefix := parentPath
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(child, prefix)
}

// joinVirtualPath appends one name to a virtual directory path without
// doubling the separator, so the root ("tie:/") joins cleanly.
func joinVirtualPath(parentPath, name string) string {
	if strings.HasSuffix(parentPath, "/") {
		return parentPath + name
	}
	return parentPath + "/" + name
}

// firstOf returns the first non-empty string.
func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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
	isTiedir := RowHas(row, RelType, str(TieDirectory)) ||
		RowFirst(row, RelMediaType) == "inode/directory"
	var missing []string
	required := []Relation{RelFilename, RelFilesize}
	if !isTiedir {
		required = append(required, RelMediaType)
	}
	for _, rel := range required {
		if len(RowValues(row, rel)) == 0 {
			missing = append(missing, string(rel))
		}
	}
	if !isTiedir {
		// A real file should carry a concrete media tie-type beyond the
		// structural "file" marker (image-file, audio-file, ...); only the
		// marker means the type was never classified.
		types := RowValues(row, RelType)
		if len(types) == 0 || (len(types) == 1 && types[0] == str(TieFile)) {
			missing = append(missing, string(RelType))
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
		for _, p := range RowValues(row, RelParent) {
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

// blobCheckWorkers bounds how many blob HEADs the check keeps in flight. One at
// a time, a 1M-file check costs 1M round trips — hours; 16 concurrent HEADs
// bring it to minutes without hammering the filehost (§5.2 item 7).
const blobCheckWorkers = 16

// checkBlobsExist stats every file's content hash on each configured default
// filehost and returns the hashes absent from all of them. One HEAD request per
// hash; the work runs through a small worker pool and the result is sorted, so
// it is the same list whatever the scheduling.
func (tie *TieClient) checkBlobsExist(files []Row) ([]string, error) {
	// The first host of the collection being verified, not of the client's
	// bound collection: `tie verify -c other --check-blobs` has to ask the
	// filehost that collection writes to (§8 S11). Only the first host is
	// asked, because content-addressed blobs are identical across hosts and
	// presence on any one is what matters for reachability.
	host, err := tie.ResolveHost("")
	if err != nil {
		return nil, err
	}
	fh := FileHostClient(host)

	var (
		wg       sync.WaitGroup
		jobs     = make(chan string, blobCheckWorkers*2)
		results  = make([][]string, blobCheckWorkers)
		firstErr error
		errOnce  sync.Once
		// stop is what the workers poll: firstErr itself is only safe to read
		// after wg.Wait.
		stop atomic.Bool
	)
	for w := 0; w < blobCheckWorkers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for hash := range jobs {
				// After the first error the workers keep draining (so the
				// feeder never blocks) but do no more work.
				if stop.Load() {
					continue
				}
				ok, _, err := fh.Stat(hash)
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						stop.Store(true)
					})
					continue
				}
				if !ok {
					results[w] = append(results[w], hash)
				}
			}
		}(w)
	}
	for _, row := range files {
		hash := row.Key
		if len(hash) != 64 {
			continue // not a content hash (shouldn't happen for a file); skip
		}
		jobs <- hash
	}
	close(jobs)
	wg.Wait()

	var missing []string
	for _, r := range results {
		missing = append(missing, r...)
	}
	sort.Strings(missing)
	if firstErr != nil {
		return missing, firstErr
	}
	return missing, nil
}

// StatBlob issues a HEAD for hash against the first configured default filehost
// and reports whether the blob is present and its on-disk byte size (from the
// Content-Length header). A 404 returns (false, 0, nil); any other non-200
// status or transport error is surfaced as an error.
func (tc *TieClient) StatBlob(hash string) (exists bool, size int64, err error) {
	fh, err := tc.BlobClient("")
	if err != nil {
		return false, 0, err
	}
	return fh.Stat(hash)
}

// RepairOrphans re-homes every orphan in rep under a timestamped restored/
// directory, making them reachable from the tree root again. It is the explicit,
// mutating counterpart to Verify and is only ever called at the user's request
// (`tie verify --repair`). Only orphans are repaired — duplicate paths, cycles,
// dangling refs and missing metadata are reported but left for a human decision.
//
// Each orphan is re-parented where its own triples say it belongs. A directory
// with a `path` triple goes back under the directory that path names — created
// by MkTieDirAll when it is gone as well — because its path is the record of
// where it was, and re-homing it flat somewhere else leaves a node whose path
// and parent edge contradict each other (#120). Everything else goes under
// tie:/restored/<date>/: files have no path triple of their own (their position
// is only their parent edge), and a directory whose path is gone too has nothing
// to go by. Nothing else about the orphan is touched, and no edge is removed.
//
// rep should come from a just-run Verify on the same collection; stale reports
// are harmless (re-parenting an already-parented node just adds another parent
// edge), but a fresh pass avoids re-homing nodes another client already fixed.
// RepairOrphans re-homes the orphan files and directories verify found in the
// client's scoped collection, under destDir.
func (tie *TieClient) RepairOrphans(rep *VerifyReport, destDir string) (int, error) {
	if rep == nil || (len(rep.OrphanDirs) == 0 && len(rep.OrphanFiles) == 0) {
		return 0, nil
	}
	if destDir == "" {
		destDir = FileURIScheme + "/restored/" + time.Now().Format("2006-01-02")
	}
	if !strings.HasPrefix(destDir, FileURIScheme) {
		destDir = FileURIScheme + "/" + destDir
	}
	batch := tie.NewBatch()
	n := 0
	destUID := DirUID("")
	recovery := func() (DirUID, error) {
		if destUID == "" {
			uid, err := tie.MkTieDirAll(destDir)
			if err != nil {
				return "", err
			}
			destUID = uid
		}
		return destUID, nil
	}
	for _, uid := range rep.OrphanDirs {
		// A directory that still carries a path triple says where it belonged,
		// so put it back there — creating the chain when the parent is gone too
		// — instead of moving it somewhere its own path contradicts (#120).
		if row, err := tie.Get(string(uid)); err == nil {
			if ps := RowValues(row, RelPath); len(ps) > 0 {
				parentPath := FileURIScheme + virtualDir(ps[0])
				if parentPath != ps[0] {
					puid, err := tie.MkTieDirAll(parentPath)
					if err != nil {
						return n, err
					}
					if puid != uid {
						batch.Add(string(uid), string(RelParent), string(puid))
						n++
						continue
					}
				}
			}
		}
		// No path to go by (or only itself): the recovery dir is the one place
		// known to be reachable from the root.
		puid, err := recovery()
		if err != nil {
			return n, err
		}
		batch.Add(string(uid), string(RelParent), string(puid))
		n++
	}
	for _, hash := range rep.OrphanFiles {
		puid, err := recovery()
		if err != nil {
			return n, err
		}
		batch.Add(hash, string(RelParent), string(puid))
		n++
	}
	if _, err := tie.Batch(batch); err != nil {
		return 0, err
	}
	if err := tie.Sync(); err != nil {
		return 0, err
	}
	return n, nil
}
