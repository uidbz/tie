// Command repair fixes the corruption classes `tie verify` reports but never
// auto-fixes: dangling parent references, ghost directories (nodes with
// tie-type directory but no path — half-written imports), and files with
// incomplete metadata. It complements `verify --repair` (which only re-homes
// orphans) and is meant to run after it.
//
// The tool is dry-run by default; --apply performs the mutations. Every
// applied mutation is journaled as TSV (op, key, relation, value) so it can
// be replayed in reverse via `tie restore` if a mistake is found — and the
// pre-repair `tie dump` of the collection is the full backstop.
//
// Repair policy (additive-first; deletes only for unreachable leftovers):
//
//   - Dangling parent ref (child -> P, P not a live dir): re-parent the child
//     to P's nearest live ancestor (walking P's parent chain); if none exists,
//     re-parent under --dest (the same restored/<date> dir verify uses).
//   - Ghost node (no path, no file metadata, no tiedir-hash referrer, no
//     remaining children): delete all its triples. Ghosts seed from the
//     parents of dangling refs and from dir-typed metadata gaps; children of a
//     ghost get re-parented in the same fixed-point loop, so chains of
//     invisible (tie-type-less) ghost nodes resolve bottom-up. A node
//     referenced by a "tiedir-hash" edge is a directory's content snapshot,
//     not a ghost, and is never deleted.
//   - File missing filename/filesize/media-type/tie-type: re-derive from the
//     blob (size via HEAD, media-type/tie-type by sniffing the leading bytes).
//     A missing filename is reconstructed as name+extension when a name
//     triple exists.
//   - File with no filename AND no name, or whose blob is absent from the
//     filehost: unreachable/unrecoverable leftover — delete all its triples.
package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/h2non/filetype"
	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/client"
)

const (
	relParent     = "parent"
	relPath       = "path"
	relTieType    = "tie-type"
	relFilename   = "filename"
	relName       = "name"
	relFilesize   = "filesize"
	relMediaType  = "media-type"
	relTiedirHash = "tiedir-hash"
	typeDirectory = "directory"
)

// op is one planned mutation; the journal records these as they are applied.
type op struct {
	kind     string // api.BatchAdd / api.BatchDelete
	key      string
	relation string
	value    string
	note     string
}

type repair struct {
	tie        *client.TieClient
	collection string
	dest       string
	apply      bool
	journal    *csv.Writer

	liveDir   map[string]bool   // DirUIDs carrying a path triple
	ghosts    map[string]bool   // candidate ghost nodes
	done      map[string]bool   // ghosts settled (deleted, file-like, or gone)
	protected map[string]bool   // nodes referenced by a tiedir-hash edge
	ancestor  map[string]string // cached nearest live ancestor per ghost
	handled   map[string]bool   // child+ghost edges already re-parented (dry-run)
	blobHC    *http.Client      // shared filehost client (per-subject clients fd-exhaust)
	blobURL   string            // first resolved filehost URL
	planned   int               // dry-run op counter
	applied   int
}

func main() {
	var (
		configFlag  = flag.String("C", "config.toml", "config file to load")
		collection  = flag.String("c", "", "collection to repair (required)")
		apply       = flag.Bool("apply", false, "perform the mutations (default: dry-run)")
		dest        = flag.String("dest", "", "fallback parent for children with no live ancestor (default: tie:/restored/<today>)")
		journalPath = flag.String("journal", "", "TSV journal of applied mutations (default: repair-journal-<collection>.tsv)")
	)
	flag.Parse()
	if *collection == "" {
		fmt.Fprintln(os.Stderr, "repair: -c <collection> is required")
		os.Exit(2)
	}
	if *dest == "" {
		*dest = client.FileURIScheme + "/restored/" + time.Now().Format("2006-01-02")
	}
	if *journalPath == "" {
		*journalPath = "repair-journal-" + *collection + ".tsv"
	}

	cfg, err := client.LoadConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair: loading config:", err)
		os.Exit(1)
	}
	r := &repair{
		tie:        client.NewTieClientFor(cfg, *collection),
		collection: *collection,
		dest:       *dest,
		apply:      *apply,
		liveDir:    map[string]bool{},
		ghosts:     map[string]bool{},
		done:       map[string]bool{},
		protected:  map[string]bool{},
		ancestor:   map[string]string{},
		handled:    map[string]bool{},
	}

	if *apply {
		f, err := os.Create(*journalPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repair: creating journal:", err)
			os.Exit(1)
		}
		defer f.Close()
		r.journal = csv.NewWriter(f)
		r.journal.Comma = '\t'
		defer r.journal.Flush()
	}

	rep, err := r.tie.Verify(*collection, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair: verify:", err)
		os.Exit(1)
	}
	fmt.Printf("before: %d problems (%d dangling refs, %d metadata gaps, %d orphan dirs, %d orphan files)\n",
		rep.Problems(), len(rep.DanglingParentRefs), len(rep.MissingMetadata), len(rep.OrphanDirs), len(rep.OrphanFiles))

	if err := r.loadLiveDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "repair: loading dir universe:", err)
		os.Exit(1)
	}

	// Seed ghost candidates: the target of every dangling ref, plus every
	// dir-typed node verify flagged as a metadata gap (no path => not a tree
	// dir; a leftover of a half-written import).
	for _, ref := range rep.DanglingParentRefs {
		r.ghosts[ref.Parent] = true
	}
	var files []client.MetadataGap
	for _, gap := range rep.MissingMetadata {
		if r.ghosts[gap.Subject] {
			continue // already seeded as a ghost (dangling parent); the ghost loop owns it
		}
		row, err := r.tie.GetIn(*collection, gap.Subject)
		if err != nil {
			continue // raced away; the final verify will report what's left
		}
		if client.RowHas(row, relTieType, typeDirectory) && len(client.RowValues(row, relPath)) == 0 {
			r.ghosts[gap.Subject] = true
		} else {
			files = append(files, gap)
		}
	}

	// Re-parent every verify-reported dangling ref directly: the ghost loop
	// below relies on reverse parent queries (childrenOf), which themselves
	// can be victims of the reverse-index inconsistency (live entries whose
	// reverse index no longer lists them). The report's child+parent pairs
	// come from forward expands, so they are reliable.
	var phase1 []op
	for _, ref := range rep.DanglingParentRefs {
		r.handled[ref.Child+"\x00"+ref.Parent] = true
		phase1 = append(phase1, op{api.BatchDelete, ref.Child, relParent, ref.Parent, "dangling ref"})
		if anc := r.nearestLiveAncestor(ref.Parent, ref.Child); anc != "" && anc != ref.Child {
			phase1 = append(phase1, op{api.BatchAdd, ref.Child, relParent, anc, "re-parent to live ancestor of " + short(ref.Parent)})
		} else {
			phase1 = append(phase1, op{api.BatchAdd, ref.Child, relParent, "DEST", "re-parent to " + r.dest + " (no live ancestor)"})
		}
	}
	r.run("re-parent", phase1)

	// Ghost fixed-point: delete each childless ghost. Re-parenting a child
	// that is itself an invisible ghost (no tie-type, hence absent from
	// verify's universe) enqueues it, so ghost chains resolve bottom-up.
	for round := 0; round < 20; round++ {
		var ops []op
		for g := range r.ghosts {
			if r.done[g] || r.liveDir[g] || r.protected[g] {
				continue
			}
			row, err := r.tie.GetIn(r.collection, g)
			if err != nil {
				r.done[g] = true // already gone
				continue
			}
			fileLikeG := fileLike(row)
			children := r.childrenOf(g)
			moved := false
			for _, child := range children {
				edge := child + "\x00" + g
				if r.handled[edge] {
					continue
				}
				r.handled[edge] = true
				moved = true
				ops = append(ops, op{api.BatchDelete, child, relParent, g, "dangling ref"})
				if anc := r.nearestLiveAncestor(g, child); anc != "" && anc != child {
					ops = append(ops, op{api.BatchAdd, child, relParent, anc, "re-parent to live ancestor of " + short(g)})
				} else {
					ops = append(ops, op{api.BatchAdd, child, relParent, "DEST", "re-parent to " + r.dest + " (no live ancestor)"})
				}
				// A child that is no live dir and not a real file is itself
				// a ghost — enqueue so chains resolve.
				if !r.liveDir[child] && !r.ghosts[child] {
					if crow, err := r.tie.GetIn(r.collection, child); err == nil && !fileLike(crow) {
						r.ghosts[child] = true
					}
				}
			}
			if moved {
				continue // delete on a later round, once childless
			}
			if fileLikeG {
				r.done[g] = true // a real file someone parented to; children moved, node stays
				continue
			}
			if ok := r.noTiedirRefs(g); !ok {
				continue
			}
			// Definitive ghost test: a node whose key exists as a blob on the
			// filehost is content (a real file), never a ghost — dir UIDs are
			// random and never uploaded. fileLike alone is not enough: an
			// import interrupted early can leave a file with only a name.
			exists, _, err := r.statBlob(g)
			if err == nil && exists {
				r.done[g] = true
				continue
			}
			del, err := r.deleteAll(g, "ghost node (no path, no children)")
			if err != nil {
				fmt.Fprintf(os.Stderr, "repair: expanding ghost %s: %v\n", short(g), err)
				continue
			}
			ops = append(ops, del...)
			r.done[g] = true
		}
		if len(ops) == 0 {
			break
		}
		r.run("ghosts", ops)
	}

	// Files: re-derive missing metadata from the blob, or delete
	// unrecoverable leftovers.
	var fix, garbage []op
	for _, gap := range files {
		ops, err := r.fixFile(gap.Subject)
		if err != nil {
			fmt.Fprintf(os.Stderr, "repair: file %s: %v\n", short(gap.Subject), err)
			continue
		}
		if len(ops) > 0 && ops[0].note == "garbage" {
			garbage = append(garbage, ops...)
		} else {
			fix = append(fix, ops...)
		}
	}
	r.run("restore file metadata", fix)
	r.run("delete garbage leftovers", garbage)

	// Final state.
	rep2, err := r.tie.Verify(*collection, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair: final verify:", err)
		os.Exit(1)
	}
	fmt.Printf("after:  %d problems (%d dangling refs, %d metadata gaps, %d orphan dirs, %d orphan files)\n",
		rep2.Problems(), len(rep2.DanglingParentRefs), len(rep2.MissingMetadata), len(rep2.OrphanDirs), len(rep2.OrphanFiles))
	if !r.apply {
		fmt.Printf("dry-run: %d op(s) planned; re-run with --apply to execute (journal: %s)\n", r.planned, *journalPath)
	} else {
		fmt.Printf("applied %d op(s); journal: %s\n", r.applied, *journalPath)
	}
}

// loadLiveDirs builds the set of DirUIDs that carry a path triple (the same
// universe verify partitions on).
func (r *repair) loadLiveDirs() error {
	rows, _, err := r.tie.QueryIn(r.collection, client.QuerySpec{
		Terms:   []string{typeDirectory},
		Filter:  relTieType,
		Reverse: true,
		Expand:  true,
		Limit:   -1,
	})
	if err != nil && err != client.ErrNotFound {
		return err
	}
	for _, row := range rows {
		if len(client.RowValues(row, relPath)) > 0 {
			r.liveDir[row.Key] = true
		}
	}
	return nil
}

// fileLike reports whether a row carries the metadata of a real file (as
// opposed to a ghost node, which holds structural triples at most).
func fileLike(row client.Row) bool {
	return len(client.RowValues(row, relFilename)) > 0 ||
		len(client.RowValues(row, relName)) > 0 ||
		len(client.RowValues(row, relFilesize)) > 0 ||
		len(client.RowValues(row, relMediaType)) > 0 ||
		client.RowHas(row, relTieType, "file")
}

// childrenOf returns the keys of every node with a parent edge to uid.
func (r *repair) childrenOf(uid string) []string {
	rows, _, err := r.tie.QueryIn(r.collection, client.QuerySpec{
		Terms: []string{uid}, Filter: relParent, Reverse: true, Limit: -1,
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
// (cached per ghost). Returns "" when the chain dies out, loops, or passes
// through `guard` (the child being re-parented — accepting it as ancestor
// would close a parent cycle; the caller then falls back to the dest dir).
func (r *repair) nearestLiveAncestor(uid string, guard ...string) string {
	if a, ok := r.ancestor[uid]; ok {
		return a
	}
	guarded := len(guard) > 0
	seen := map[string]bool{}
	cur := uid
	for cur != "" && !seen[cur] {
		seen[cur] = true
		if guarded && cur == guard[0] {
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
		cur = client.RowFirst(row, relParent)
	}
	r.ancestor[uid] = ""
	return ""
}

// noTiedirRefs reports (and remembers) whether no node references uid via a
// tiedir-hash edge — such a node is a directory's content snapshot, not a
// ghost.
func (r *repair) noTiedirRefs(uid string) bool {
	rows, _, err := r.tie.QueryIn(r.collection, client.QuerySpec{
		Terms: []string{uid}, Filter: relTiedirHash, Reverse: true, Limit: -1,
	})
	if err == nil && len(rows) > 0 {
		r.protected[uid] = true
		return false
	}
	return true
}

// deleteAll expands every triple of uid into a delete op.
func (r *repair) deleteAll(uid, note string) ([]op, error) {
	row, err := r.tie.GetIn(r.collection, uid)
	if err != nil {
		return nil, err
	}
	var ops []op
	rels := make([]string, 0, len(row.Attributes))
	for rel := range row.Attributes {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		for _, val := range row.Attributes[rel] {
			ops = append(ops, op{api.BatchDelete, uid, rel, val, note})
		}
	}
	return ops, nil
}

// fixFile plans the repair of one incompletely-imported file: re-derive what
// can be derived from the blob, or delete the subject's triples when the file
// is unrecoverable (no name at all, or blob absent from the filehost).
func (r *repair) fixFile(subject string) ([]op, error) {
	row, err := r.tie.GetIn(r.collection, subject)
	if err != nil {
		return nil, err
	}
	filename := client.RowFirst(row, relFilename)
	name := client.RowFirst(row, relName)

	exists, size, err := r.statBlob(subject)
	if err != nil {
		return nil, err
	}
	if !exists {
		// Blob gone: the triples are all that was left, and they name nothing.
		ops, err := r.deleteAll(subject, "garbage")
		return ops, err
	}
	if filename == "" && name == "" {
		// Nameless leftover: no way to present or place it.
		ops, err := r.deleteAll(subject, "garbage")
		return ops, err
	}

	var ops []op
	if len(client.RowValues(row, relFilesize)) == 0 {
		ops = append(ops, op{api.BatchAdd, subject, relFilesize, strconv.FormatInt(size, 10), "size from blob HEAD"})
	}
	needMedia := len(client.RowValues(row, relMediaType)) == 0
	// Mirror verify's rule: a real file needs a concrete media tie-type beyond
	// the structural "file" marker.
	types := client.RowValues(row, relTieType)
	needType := len(types) == 0 || (len(types) == 1 && types[0] == "file")
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
		tieType, err := client.GetTieType(bytes.NewReader(head))
		if err != nil {
			return nil, err
		}
		if needMedia {
			ops = append(ops, op{api.BatchAdd, subject, relMediaType, mediaType, "sniffed from blob"})
		}
		if needType {
			ops = append(ops, op{api.BatchAdd, subject, relTieType, tieType.String(), "sniffed from blob"})
		}
	}
	if filename == "" && name != "" {
		if mediaType == "" {
			mediaType = client.RowFirst(row, relMediaType)
		}
		ops = append(ops, op{api.BatchAdd, subject, relFilename, name + extFor(mediaType), "reconstructed from name"})
	}
	return ops, nil
}

// blobClient returns the shared filehost HTTP client + base URL, built once
// from the first resolved default host. Per-subject clients (as
// client.StatBlob/HTTPClientFor would each create) exhaust fds on large
// collections via TIME_WAIT pileup.
func (r *repair) blobClient() (*http.Client, string, error) {
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
	r.blobHC = client.HTTPClientFor(host)
	r.blobURL = host.URL
	return r.blobHC, r.blobURL, nil
}
// statBlob is client.StatBlob reimplemented over the shared client: existence
// + on-disk size from a HEAD, per-collection host resolution done once.
func (r *repair) statBlob(hash string) (bool, int64, error) {
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

// blobHead fetches the first bytes of a blob for sniffing, via an HTTP Range
// request (the filehost serves blobs with http.ServeFile, which honors Range;
// if it didn't, we simply read the prefix and close).
func (r *repair) blobHead(hash string) ([]byte, error) {
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

// run executes (or, in dry-run, prints) a batch of planned ops, journaling
// every applied op. The DEST placeholder resolves to the restore dir's UID,
// created on first use.
func (r *repair) run(label string, ops []op) {
	if len(ops) == 0 {
		return
	}
	fmt.Printf("phase %-24s %d op(s)\n", label+":", len(ops))
	if !r.apply {
		for _, o := range ops {
			fmt.Printf("  %-6s %s %-9s %s   # %s\n", o.kind, short(o.key), o.relation, shortVal(o.value), o.note)
			r.planned++
		}
		return
	}
	const chunk = 500
	for i := 0; i < len(ops); i += chunk {
		batch := r.tie.NewBatchIn(r.collection)
		for _, o := range ops[i:min(i+chunk, len(ops))] {
			val := o.value
			if val == "DEST" {
				uid, err := r.tie.MkTieDirAll(r.dest)
				if err != nil {
					fmt.Fprintln(os.Stderr, "repair: creating restore dir:", err)
					os.Exit(1)
				}
				val = string(uid)
			}
			switch o.kind {
			case api.BatchAdd:
				batch.Add(o.key, o.relation, val)
			case api.BatchDelete:
				batch.Delete(o.key, o.relation, val)
			}
			r.journal.Write([]string{time.Now().Format(time.RFC3339), o.kind, o.key, o.relation, val, o.note})
			r.applied++
		}
		if _, err := r.tie.Batch(batch); err != nil {
			fmt.Fprintf(os.Stderr, "repair: batch in phase %q: %v\n", label, err)
			os.Exit(1)
		}
	}
	if err := r.tie.SyncIn(r.collection); err != nil {
		fmt.Fprintln(os.Stderr, "repair: sync:", err)
		os.Exit(1)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func shortVal(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
