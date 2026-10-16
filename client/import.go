package client

import (
	"fmt"
	"path"
	"strings"

	"github.com/uidbz/tie/metadata"
)

// One import engine behind every import front end (§7.9).
//
// ImportDir, ImportFile and the album importer used to carry their own copies of
// the same pipeline: the batch-and-flush closure, the directory-chain creation,
// the albumMeta aggregation, the per-file classify-and-tag step, and the
// dir-type/index stamp at the end. The copies drifted (the album import never
// reconciled, never printed, and named its directories differently). A front
// end now only builds a plan — where the tree goes, which nodes exist, which
// files sit in them, and what to do afterwards — and executeImport runs it.

// planItem is one file an import places, already uploaded to the filehost.
type planItem struct {
	Src       string // local path: the source of classification and metadata
	Rel       string // path below the import root, slash-separated ("disc1/song.flac")
	Hash      string // content address on the filehost
	Size      int
	MediaType string
	Metadata  metadata.Media
	TieType   TieType // resolved by the front end, which reads the file
	IsDir     bool
}

// planDir is one directory node of the imported tree. Every directory the plan
// touches must appear here: it is what gives the node its name, size and tags,
// and what reconciliation and the album aggregation are keyed by.
type planDir struct {
	Rel        string // "." for the root, else slash-separated below it
	Name       string // display name
	Size       int    // bytes recorded on the node
	TiedirHash string // immutable tiedir snapshot to link, when the import has one
}

// ImportedItem is one item an import placed, reported to TieClient.OnImport
// once the batch carrying it has been committed. Path is the item's virtual
// path below the import root ("/music/album/01.flac"); a plan rooted at an
// existing node instead of a virtual path (ImportFile) names the item by the
// local file it came from.
type ImportedItem struct {
	Hash    string
	Path    string
	TieType TieType
	Size    int
}

// importPlan is the shared shape of an import.
type importPlan struct {
	Root    string // virtual root path ("tie:/albums/x"); ignored when RootUID is set
	RootUID DirUID // an existing root node to place items in (ImportFile)

	Dirs  []planDir  // every directory node, in any order
	Items []planItem // the files, in any order

	Tags []string

	// StampDirType writes DirType on the root even when it is empty (what
	// ImportDir and the album importer do); ImportFile leaves it alone.
	StampDirType bool
	DirType      string
	Index        bool // run IndexCollection on the root regardless of its type
	Reconcile    bool // version away children this import did not place
	AddedAt      bool // stamp added-at on each item
}

// executeImport runs a plan: place every file under its directory (creating the
// chain on demand), tag in chunked batches, aggregate album metadata onto the
// directory nodes, then optionally reconcile, stamp the dir-type and index.
func (tie *TieClient) executeImport(p importPlan) error {
	// Tagging is the dominant request count on a large import: one Batch per
	// file would be thousands of tiny sequential POSTs. Accumulate the ops and
	// flush in chunks; flushThreshold bounds the in-flight batch (memory and
	// request size), and api.Batch applies every op under a single Sync.
	const flushThreshold = 1000
	batch := tie.NewBatch()
	// reported holds the items whose ops are in the open batch. They are
	// announced only when that batch commits, so a reported item is a stored
	// item — a pipeline reading `tie import`'s stdout never sees one that a
	// later failure rolled back.
	var reported []ImportedItem
	flush := func() error {
		if len(batch.Ops) == 0 {
			return nil
		}
		if _, err := tie.Batch(batch); err != nil {
			return err
		}
		batch = tie.NewBatch()
		if tie.OnImport != nil {
			for _, it := range reported {
				tie.OnImport(it)
			}
		}
		reported = reported[:0]
		return nil
	}

	dirUIDs := make(map[string]DirUID, len(p.Dirs)+1)
	albums := make(map[string]*albumMeta)
	// want records, per directory DirUID, the content hashes this import placed
	// there — the source of truth for reconciliation. Keying on the hash, not
	// the filename, is correct even when one hash has several names or parents.
	want := make(map[DirUID]map[string]bool)

	// uidFor resolves a relative directory to its DirUID, creating it and any
	// missing ancestor. Naming and tagging a node is the Dirs pass below, so a
	// placeholder created here is harmless.
	var uidFor func(rel string) (DirUID, error)
	uidFor = func(rel string) (DirUID, error) {
		if rel == "." || rel == "" {
			if uid, ok := dirUIDs["."]; ok {
				return uid, nil
			}
			uid := p.RootUID
			if uid == "" && p.Root != "" {
				created, err := tie.MkTieDirAll(p.Root)
				if err != nil {
					return "", err
				}
				uid = created
			}
			dirUIDs["."] = uid
			return uid, nil
		}
		if uid, ok := dirUIDs[rel]; ok {
			return uid, nil
		}
		if p.RootUID != "" {
			return "", fmt.Errorf("import: %s: a plan rooted at an existing node cannot create subdirectories", rel)
		}
		if _, err := uidFor(path.Dir(rel)); err != nil {
			return "", err
		}
		uid, err := tie.MkTieDirAll(p.Root + "/" + rel)
		if err != nil {
			return "", err
		}
		dirUIDs[rel] = uid
		return uid, nil
	}

	for _, it := range p.Items {
		rel := path.Dir(it.Rel)
		parent, err := uidFor(rel)
		if err != nil {
			return fmt.Errorf("import %s: parent dir: %w", it.Rel, err)
		}
		info := TagInfo{
			Hash:      it.Hash,
			File:      it.Src,
			Size:      it.Size,
			MediaType: it.MediaType,
			Directory: parent,
			TieType:   it.TieType,
			Tags:      p.Tags,
			IsDir:     it.IsDir,
			Metadata:  it.Metadata,
		}
		appendTagOps(batch, info)
		if tie.OnImport != nil {
			vpath := it.Src
			if p.Root != "" {
				vpath = strings.TrimPrefix(p.Root, FileURIScheme) + "/" + it.Rel
			}
			reported = append(reported, ImportedItem{
				Hash: it.Hash, Path: vpath, TieType: it.TieType, Size: it.Size,
			})
		}
		if albums[rel] == nil {
			albums[rel] = &albumMeta{}
		}
		albums[rel].add(info.Metadata)
		if want[parent] == nil {
			want[parent] = make(map[string]bool)
		}
		want[parent][it.Hash] = true

		if len(batch.Ops) >= flushThreshold {
			if err := flush(); err != nil {
				return fmt.Errorf("import %s: tag batch: %w", it.Rel, err)
			}
		}
		if p.AddedAt {
			// added-at is the stable "date added" (media.go): written once,
			// never replaced by a re-import the way tag-date is. It needs the
			// item's own triples committed first.
			if err := flush(); err != nil {
				return fmt.Errorf("import %s: tag batch: %w", it.Rel, err)
			}
			if err := tie.EnsureAddedAt(it.Hash); err != nil {
				return err
			}
		}
	}

	// Directory nodes: name, size, tags, the album aggregates their files
	// produced, and the immutable tiedir snapshot they were imported from.
	for _, d := range p.Dirs {
		uid, err := uidFor(d.Rel)
		if err != nil {
			return fmt.Errorf("import dir %s: %w", d.Rel, err)
		}
		appendTagDirOps(batch, uid, d.Name, d.Size, p.Tags)
		if am := albums[d.Rel]; am != nil {
			am.writeOps(batch, uid)
		}
		if d.TiedirHash != "" {
			// Link the DirUID to its snapshot blob so the content-addressed
			// snapshot stays reachable from the live node.
			batch.Add(string(uid), str(TieTiedirHash), d.TiedirHash)
		}
		if len(batch.Ops) >= flushThreshold {
			if err := flush(); err != nil {
				return fmt.Errorf("import dir %s: tag batch: %w", d.Rel, err)
			}
		}
	}

	// Everything must be committed before reconciliation: reconcileDir queries
	// the committed children of each touched directory.
	if err := flush(); err != nil {
		return fmt.Errorf("import: final tag batch: %w", err)
	}

	rootUID, err := uidFor(".")
	if err != nil {
		return fmt.Errorf("import: root dir: %w", err)
	}

	// Reconcile every directory this import touched: superseded or orphaned
	// file children move into per-file "_prev" history dirs (or are deleted
	// when history is off), honoring the configured retention count. A
	// directory that received no files is still reconciled with an empty want
	// set, so a file deleted down to zero is handled.
	if p.Reconcile {
		for rel, uid := range dirUIDs {
			if err := reconcileDir(tie, uid, want[uid], tie.Config.PrevVersions); err != nil {
				return fmt.Errorf("import: reconcile %s: %w", rel, err)
			}
		}
	}

	if p.StampDirType {
		if err := tie.SetDirType(rootUID, p.DirType); err != nil {
			return fmt.Errorf("import: set dir type: %w", err)
		}
	}
	if p.Index || IsCollectionLabel(p.DirType) {
		if _, err := tie.IndexCollection(rootUID, IndexOptions{}); err != nil {
			return fmt.Errorf("import: index collection: %w", err)
		}
	}
	return nil
}
