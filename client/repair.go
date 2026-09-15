package client

// repair.go fixes the corruption classes Verify reports but RepairOrphans
// never touches: dangling parent references, ghost directories (nodes with
// tie-type directory but no path — half-written imports), and files with
// incomplete metadata. It is the destructive half of `tie verify` (`--fix`),
// separate from the additive `--repair` (index repair + orphan re-homing), and
// is plan-first: RepairTree returns the operations it would apply unless
// RepairOptions.Apply is set, and journals every applied operation as TSV so a
// mistake can be reversed via `tie restore` — with the pre-repair `tie dump`
// as the full backstop.
//
// Repair policy (additive-first; deletes only for unreachable leftovers):
//
//   - Dangling parent ref (child -> P, P not a live dir): re-parent the child
//     to P's nearest live ancestor (walking P's parent chain); if none exists,
//     re-parent under Dest (the same restored/<date> dir RepairOrphans uses).
//   - Ghost node (no path, no file metadata, no tiedir-hash referrer, no
//     remaining children, no blob on the filehost): delete all its triples.
//     Ghosts seed from the parents of dangling refs and from dir-typed
//     metadata gaps; children of a ghost get re-parented in the same
//     fixed-point loop, so chains of invisible (tie-type-less) ghost nodes
//     resolve bottom-up. A node referenced by a "tiedir-hash" edge is a
//     directory's content snapshot, not a ghost, and is never deleted.
//   - File missing filename/filesize/media-type/tie-type: re-derive from the
//     blob (size via HEAD, media-type/tie-type by sniffing the leading bytes).
//     A missing filename is reconstructed as name+extension when a name
//     triple exists.
//   - File with no filename AND no name, or whose blob is absent from the
//     filehost: unreachable/unrecoverable leftover — delete all its triples.

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/h2non/filetype"
	"github.com/uidbz/tie/api"
)

// RepairOptions configures RepairTree.
type RepairOptions struct {
	// Dest is the fallback parent for children with no live ancestor. Empty
	// means tie:/restored/<today>, the directory RepairOrphans also uses.
	Dest string
	// Apply performs the mutations. When false RepairTree only plans.
	Apply bool
	// Journal receives one TSV line (time, op, key, relation, value, note) per
	// applied operation. Ignored unless Apply is set. May be nil.
	Journal io.Writer
	// Progress receives one line per phase. May be nil.
	Progress io.Writer
}

// RepairOp is one planned or applied triple mutation.
type RepairOp struct {
	Kind     string // api.BatchAdd or api.BatchDelete
	Key      string
	Relation string
	Value    string // "DEST" is a placeholder resolved to Dest's UID at apply time
	Note     string
}

// RepairResult reports what RepairTree planned or did.
type RepairResult struct {
	// Planned holds every operation, in phase order. In plan mode these are
	// what --apply would do; in apply mode they were all executed.
	Planned []RepairOp
	// Applied is the number of operations executed (0 in plan mode).
	Applied int
}

// repairSession carries the per-run caches of RepairTree.
type repairSession struct {
	tie        *TieClient
	collection string
	opts       RepairOptions
	res        *RepairResult
	journal    *csv.Writer

	liveDir   map[string]bool   // DirUIDs carrying a path triple
	ghosts    map[string]bool   // ghost candidates
	done      map[string]bool   // ghosts resolved (deleted, or found to be real)
	protected map[string]bool   // referenced by a tiedir-hash edge: never deleted
	ancestor  map[string]string // nearestLiveAncestor cache
	handled   map[string]bool   // child\x00parent edges already re-parented
	destUID   string

	blobHC  *http.Client
	blobURL string
}

// RepairTree fixes the dangling-reference, ghost-node and file-metadata
// problems in rep, which should come from a just-run Verify on the same
// collection. Without opts.Apply it returns the plan and changes nothing. It
// does not re-home orphans (RepairOrphans) or touch the index (CheckIndex);
// `tie verify --fix` composes all three.
func (tie *TieClient) RepairTree(collection string, rep *VerifyReport, opts RepairOptions) (*RepairResult, error) {
	if opts.Dest == "" {
		opts.Dest = FileURIScheme + "/restored/" + time.Now().Format("2006-01-02")
	}
	if !strings.HasPrefix(opts.Dest, FileURIScheme) {
		opts.Dest = FileURIScheme + "/" + opts.Dest
	}
	r := &repairSession{
		tie: tie, collection: collection, opts: opts, res: &RepairResult{},
		liveDir: map[string]bool{}, ghosts: map[string]bool{}, done: map[string]bool{},
		protected: map[string]bool{}, ancestor: map[string]string{}, handled: map[string]bool{},
	}
	if opts.Apply && opts.Journal != nil {
		r.journal = csv.NewWriter(opts.Journal)
		r.journal.Comma = '\t'
		defer r.journal.Flush()
	}
	if rep == nil {
		return r.res, nil
	}

	if err := r.loadLiveDirs(); err != nil {
		return r.res, fmt.Errorf("loading directory universe: %w", err)
	}

	// Seed ghost candidates: the target of every dangling ref, plus every
	// dir-typed node verify flagged as a metadata gap (no path => not a tree
	// dir; a leftover of a half-written import).
	for _, ref := range rep.DanglingParentRefs {
		r.ghosts[ref.Parent] = true
	}
	var files []MetadataGap
	for _, gap := range rep.MissingMetadata {
		if r.ghosts[gap.Subject] {
			continue // the ghost loop owns it
		}
		row, err := tie.GetIn(collection, gap.Subject)
		if err != nil {
			continue // raced away; the final verify will report what's left
		}
		if RowHas(row, str(TieTypeProperty), str(TieDirectory)) && len(RowValues(row, str(TiePath))) == 0 {
			r.ghosts[gap.Subject] = true
		} else {
			files = append(files, gap)
		}
	}

	// Re-parent every reported dangling ref directly: the ghost loop below
	// relies on reverse parent queries (childrenOf), and the report's
	// child+parent pairs come from forward expands, which are authoritative.
	var phase1 []RepairOp
	for _, ref := range rep.DanglingParentRefs {
		r.handled[ref.Child+"\x00"+ref.Parent] = true
		phase1 = append(phase1, r.reparentOps(ref.Child, ref.Parent)...)
	}
	if err := r.run("re-parent dangling refs", phase1); err != nil {
		return r.res, err
	}

	// Ghost fixed-point: delete each childless ghost. Re-parenting a child
	// that is itself an invisible ghost (no tie-type, hence absent from
	// verify's universe) enqueues it, so ghost chains resolve bottom-up.
	for round := 0; round < 20; round++ {
		var ops []RepairOp
		for g := range r.ghosts {
			if r.done[g] || r.liveDir[g] || r.protected[g] {
				continue
			}
			row, err := tie.GetIn(collection, g)
			if err != nil {
				r.done[g] = true // already gone
				continue
			}
			moved := false
			for _, child := range r.childrenOf(g) {
				edge := child + "\x00" + g
				if r.handled[edge] {
					continue
				}
				r.handled[edge] = true
				moved = true
				ops = append(ops, r.reparentOps(child, g)...)
				// A child that is no live dir and not a real file is itself a
				// ghost — enqueue so chains resolve.
				if !r.liveDir[child] && !r.ghosts[child] {
					if crow, err := tie.GetIn(collection, child); err == nil && !fileLike(crow) {
						r.ghosts[child] = true
					}
				}
			}
			if moved {
				continue // delete on a later round, once childless
			}
			if fileLike(row) {
				r.done[g] = true // a real file someone parented to; children moved, node stays
				continue
			}
			if !r.noTiedirRefs(g) {
				continue
			}
			// Definitive ghost test: a key that exists as a blob on the
			// filehost is content (a real file), never a ghost — dir UIDs are
			// random and never uploaded. fileLike alone is not enough: an
			// import interrupted early can leave a file with only a name.
			if exists, _, err := r.statBlob(g); err == nil && exists {
				r.done[g] = true
				continue
			}
			del, err := r.deleteAll(g, "ghost node (no path, no children)")
			if err != nil {
				r.progress("expanding ghost %s: %v", short(g), err)
				continue
			}
			ops = append(ops, del...)
			r.done[g] = true
		}
		if len(ops) == 0 {
			break
		}
		if err := r.run("ghost nodes", ops); err != nil {
			return r.res, err
		}
	}

	// Files: re-derive missing metadata from the blob, or delete
	// unrecoverable leftovers.
	var fix, garbage []RepairOp
	for _, gap := range files {
		ops, err := r.fixFile(gap.Subject)
		if err != nil {
			r.progress("file %s: %v", short(gap.Subject), err)
			continue
		}
		if len(ops) > 0 && ops[0].Note == "garbage" {
			garbage = append(garbage, ops...)
		} else {
			fix = append(fix, ops...)
		}
	}
	if err := r.run("restore file metadata", fix); err != nil {
		return r.res, err
	}
	if err := r.run("delete unrecoverable leftovers", garbage); err != nil {
		return r.res, err
	}
	return r.res, nil
}

func (r *repairSession) progress(format string, args ...any) {
	if r.opts.Progress != nil {
		fmt.Fprintf(r.opts.Progress, format+"\n", args...)
	}
}

// reparentOps drops child's edge to the dead parent and adds one to the
// nearest live ancestor (or the DEST placeholder).
func (r *repairSession) reparentOps(child, parent string) []RepairOp {
	ops := []RepairOp{{api.BatchDelete, child, str(TieParent), parent, "dangling ref"}}
	if anc := r.nearestLiveAncestor(parent, child); anc != "" && anc != child {
		ops = append(ops, RepairOp{api.BatchAdd, child, str(TieParent), anc, "re-parent to live ancestor of " + short(parent)})
	} else {
		ops = append(ops, RepairOp{api.BatchAdd, child, str(TieParent), "DEST", "re-parent to " + r.opts.Dest + " (no live ancestor)"})
	}
	return ops
}

// loadLiveDirs builds the set of DirUIDs that carry a path triple (the same
// universe Verify partitions on).
func (r *repairSession) loadLiveDirs() error {
	rows, _, err := r.tie.QueryIn(r.collection, QuerySpec{
		Terms: []string{str(TieDirectory)}, Filter: str(TieTypeProperty), Reverse: true, Expand: true, Limit: -1,
	})
	if err != nil && err != ErrNotFound {
		return err
	}
	for _, row := range rows {
		if len(RowValues(row, str(TiePath))) > 0 {
			r.liveDir[row.Key] = true
		}
	}
	return nil
}

// fileLike reports whether a row carries the metadata of a real file (as
// opposed to a ghost node, which holds structural triples at most).
func fileLike(row Row) bool {
	return len(RowValues(row, str(TieFilename))) > 0 ||
		len(RowValues(row, str(TieName))) > 0 ||
		len(RowValues(row, str(TieFilesize))) > 0 ||
		len(RowValues(row, str(TieMediaType))) > 0 ||
		RowHas(row, str(TieTypeProperty), str(TieFile))
}

// childrenOf returns the keys of every node with a parent edge to uid.
func (r *repairSession) childrenOf(uid string) []string {
	rows, _, err := r.tie.QueryIn(r.collection, QuerySpec{
		Terms: []string{uid}, Filter: str(TieParent), Reverse: true, Limit: -1,
	})
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	return keys
}

// nearestLiveAncestor walks uid's parent chain until a live dir is found
// (cached per node). Returns "" when the chain dies out, loops, or passes
// through guard (the child being re-parented — accepting it as ancestor would
// close a parent cycle; the caller then falls back to Dest).
func (r *repairSession) nearestLiveAncestor(uid, guard string) string {
	if a, ok := r.ancestor[uid]; ok {
		return a
	}
	seen := map[string]bool{}
	cur := uid
	for cur != "" && !seen[cur] {
		seen[cur] = true
		if cur == guard {
			r.ancestor[uid] = ""
			return ""
		}
		if r.liveDir[cur] {
			r.ancestor[uid] = cur
			return cur
		}
		row, err := r.tie.GetIn(r.collection, cur)
		if err != nil {
			break
		}
		cur = RowFirst(row, str(TieParent))
	}
	r.ancestor[uid] = ""
	return ""
}

// noTiedirRefs reports (and remembers) whether no node references uid via a
// tiedir-hash edge — such a node is a directory's content snapshot, not a
// ghost.
func (r *repairSession) noTiedirRefs(uid string) bool {
	rows, _, err := r.tie.QueryIn(r.collection, QuerySpec{
		Terms: []string{uid}, Filter: str(TieTiedirHash), Reverse: true, Limit: -1,
	})
	if err == nil && len(rows) > 0 {
		r.protected[uid] = true
		return false
	}
	return true
}

// deleteAll expands every triple of uid into a delete op.
func (r *repairSession) deleteAll(uid, note string) ([]RepairOp, error) {
	row, err := r.tie.GetIn(r.collection, uid)
	if err != nil {
		return nil, err
	}
	rels := make([]string, 0, len(row.Attributes))
	for rel := range row.Attributes {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var ops []RepairOp
	for _, rel := range rels {
		for _, val := range row.Attributes[rel] {
			ops = append(ops, RepairOp{api.BatchDelete, uid, rel, val, note})
		}
	}
	return ops, nil
}

// fixFile plans the repair of one incompletely-imported file: re-derive what
// can be derived from the blob, or delete the subject's triples when the file
// is unrecoverable (no name at all, or blob absent from the filehost).
func (r *repairSession) fixFile(subject string) ([]RepairOp, error) {
	row, err := r.tie.GetIn(r.collection, subject)
	if err != nil {
		return nil, err
	}
	filename := RowFirst(row, str(TieFilename))
	name := RowFirst(row, str(TieName))

	exists, size, err := r.statBlob(subject)
	if err != nil {
		return nil, err
	}
	if !exists || (filename == "" && name == "") {
		// Blob gone, or a nameless leftover: nothing to present or place.
		return r.deleteAll(subject, "garbage")
	}

	var ops []RepairOp
	if len(RowValues(row, str(TieFilesize))) == 0 {
		ops = append(ops, RepairOp{api.BatchAdd, subject, str(TieFilesize), strconv.FormatInt(size, 10), "size from blob HEAD"})
	}
	needMedia := len(RowValues(row, str(TieMediaType))) == 0
	// Mirror Verify's rule: a real file needs a concrete media tie-type beyond
	// the structural "file" marker.
	types := RowValues(row, str(TieTypeProperty))
	needType := len(types) == 0 || (len(types) == 1 && types[0] == str(TieFile))
	var mediaType string
	if needMedia || needType {
		head, err := r.blobHead(subject)
		if err != nil {
			return nil, err
		}
		mediaType = "application/octet-stream"
		if t, err := filetype.Get(head); err == nil && t != filetype.Unknown {
			mediaType = t.MIME.Value
		}
		tieType, err := GetTieType(bytes.NewReader(head))
		if err != nil {
			return nil, err
		}
		if needMedia {
			ops = append(ops, RepairOp{api.BatchAdd, subject, str(TieMediaType), mediaType, "sniffed from blob"})
		}
		if needType {
			ops = append(ops, RepairOp{api.BatchAdd, subject, str(TieTypeProperty), tieType.String(), "sniffed from blob"})
		}
	}
	if filename == "" && name != "" {
		if mediaType == "" {
			mediaType = RowFirst(row, str(TieMediaType))
		}
		ops = append(ops, RepairOp{api.BatchAdd, subject, str(TieFilename), name + extFor(mediaType), "reconstructed from name"})
	}
	return ops, nil
}

// blobClient returns the shared filehost HTTP client + base URL, built once
// from the collection's first resolved host. One client for the whole run:
// per-subject clients exhaust fds on large collections via TIME_WAIT pileup.
func (r *repairSession) blobClient() (*http.Client, string, error) {
	if r.blobHC != nil {
		return r.blobHC, r.blobURL, nil
	}
	hosts := r.tie.ResolveHosts(r.collection, nil)
	if len(hosts) == 0 {
		return nil, "", fmt.Errorf("no filehosts configured")
	}
	host, ok := r.tie.Config.FileHosts[hosts[0]]
	if !ok {
		return nil, "", fmt.Errorf("filehost %q has no [FileHosts] entry", hosts[0])
	}
	r.blobHC = HTTPClientFor(host)
	r.blobURL = strings.TrimRight(host.URL, "/")
	return r.blobHC, r.blobURL, nil
}

// statBlob is StatBlob over the shared client: existence + size from a HEAD.
func (r *repairSession) statBlob(hash string) (bool, int64, error) {
	hc, base, err := r.blobClient()
	if err != nil {
		return false, 0, err
	}
	req, err := http.NewRequest(http.MethodHead, base+"/"+hash, nil)
	if err != nil {
		return false, 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, 0, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, resp.ContentLength, nil
	case http.StatusNotFound:
		return false, 0, nil
	default:
		return false, 0, fmt.Errorf("filehost stat %s: %s", short(hash), resp.Status)
	}
}

// blobHead fetches the first bytes of a blob for sniffing via an HTTP Range
// request (the filehost serves blobs with http.ServeFile, which honors Range;
// if it didn't, we simply read the prefix and close).
func (r *repairSession) blobHead(hash string) ([]byte, error) {
	hc, base, err := r.blobClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, base+"/"+hash, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-511")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("filehost GET %s: %s", short(hash), resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512))
}

// extFor maps a sniffed media type to a filename extension. Unknown types get
// none — the bare name is still a valid filename.
func extFor(mediaType string) string {
	switch mediaType {
	case "audio/mpeg":
		return ".mp3"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/x-matroska":
		return ".mkv"
	case "video/webm":
		return ".webm"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	}
	return ""
}

// run records a phase's ops and, in apply mode, executes them in batches,
// journaling each. The DEST placeholder resolves to Dest's UID, created on
// first use.
func (r *repairSession) run(label string, ops []RepairOp) error {
	if len(ops) == 0 {
		return nil
	}
	r.progress("phase %-32s %d op(s)", label+":", len(ops))
	r.res.Planned = append(r.res.Planned, ops...)
	if !r.opts.Apply {
		return nil
	}
	const chunk = 500
	for i := 0; i < len(ops); i += chunk {
		batch := r.tie.NewBatchIn(r.collection)
		for _, o := range ops[i:min(i+chunk, len(ops))] {
			val := o.Value
			if val == "DEST" {
				if r.destUID == "" {
					uid, err := r.tie.MkTieDirAll(r.opts.Dest)
					if err != nil {
						return fmt.Errorf("creating restore dir %s: %w", r.opts.Dest, err)
					}
					r.destUID = string(uid)
				}
				val = r.destUID
			}
			switch o.Kind {
			case api.BatchAdd:
				batch.Add(o.Key, o.Relation, val)
			case api.BatchDelete:
				batch.Delete(o.Key, o.Relation, val)
			}
			if r.journal != nil {
				r.journal.Write([]string{time.Now().Format(time.RFC3339), o.Kind, o.Key, o.Relation, val, o.Note})
			}
			r.res.Applied++
		}
		if _, err := r.tie.Batch(batch); err != nil {
			return fmt.Errorf("batch in phase %q: %w", label, err)
		}
	}
	return r.tie.SyncIn(r.collection)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}
