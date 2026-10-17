package client

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/io/putlib"
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

// ImportFile uploads one file and links it into `directory` (nowhere, when
// directory is empty) in the client's scoped collection. Unlike a tree import
// it places one item under an existing node, so it neither reconciles the
// directory — the item is a subset view — nor stamps a dir-type.
func (tie *TieClient) ImportFile(file string, host FileHost, tags []string, directory DirUID, forcedArchive TieType) error {
	tie.log().Debug("importing file", "path", file)
	// Classification is strict here, unlike in a tree import: nothing has been
	// uploaded yet, so an unclassifiable file is refused rather than tagged
	// unknown and left stranded on the filehost. One open answers the type and
	// the embedded tags (§5.2 item 3).
	fileType, media, err := ingest(file)
	if err != nil {
		return fmt.Errorf("import %s: classify: %w", file, err)
	}
	fileType = applyArchiveOverride(fileType, forcedArchive)
	stat, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("import %s: %w", file, err)
	}
	status := putlib.Upload(host.URL, file, putlib.PutConfig{Client: HTTPClientFor(host), Store: host.Store})
	if status.ErrorMsg != "" {
		return fmt.Errorf("uploading %s: %s", status.LastItem.Filename, status.ErrorMsg)
	}

	return tie.executeImport(importPlan{
		RootUID: directory,
		Items: []planItem{{
			Src:       file,
			Rel:       filepath.Base(file),
			Hash:      status.LastItem.Hash,
			Size:      int(stat.Size()),
			MediaType: status.LastItem.MediaType,
			Metadata:  media,
			TieType:   fileType,
			IsDir:     stat.IsDir(),
		}},
		Tags:    tags,
		AddedAt: true,
	})
}

// sanitizePathSegment makes a metadata value safe to embed in a single virtual
// path segment: path separators become spaces (so a value can't inject extra
// directory levels), "." and ".." are neutralized, control characters are
// dropped, and surrounding whitespace is trimmed.
func sanitizePathSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return ' '
		case r < 0x20:
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// renderDestTemplate expands {artist}, {albumartist}, {album}, {year}, {title},
// {track} in tmpl from m, sanitizing each value so it stays within one path
// segment. {albumartist} falls back to {artist} when no album-artist tag is
// present, so compilations without one still render. It returns an error if
// any referenced variable resolves to an empty value, so the caller can fall
// back rather than produce a path with blank segments. An unknown variable
// name is also an error.
func renderDestTemplate(tmpl string, m metadata.Media) (string, error) {
	value := func(name string) (string, bool) {
		switch name {
		case "artist":
			return sanitizePathSegment(m.Artist), true
		case "albumartist":
			albumArtist := m.AlbumArtist
			if albumArtist == "" {
				albumArtist = m.Artist
			}
			return sanitizePathSegment(albumArtist), true
		case "album":
			return sanitizePathSegment(m.Album), true
		case "title":
			return sanitizePathSegment(m.Title), true
		case "year":
			if m.Year == 0 {
				return "", true
			}
			return strconv.Itoa(m.Year), true
		case "track":
			if m.Track == 0 {
				return "", true
			}
			return strconv.Itoa(m.Track), true
		}
		return "", false
	}

	var out strings.Builder
	for i := 0; i < len(tmpl); {
		c := tmpl[i]
		if c != '{' {
			out.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated '{' in import destination template %q", tmpl)
		}
		name := tmpl[i+1 : i+end]
		v, ok := value(name)
		if !ok {
			return "", fmt.Errorf("unknown variable {%s} in import destination template %q", name, tmpl)
		}
		if v == "" {
			return "", fmt.Errorf("empty value for {%s}", name)
		}
		out.WriteString(v)
		i += end + 1
	}
	return out.String(), nil
}

// ValidateDestTemplate reports whether tmpl is a usable import destination
// template: every {variable} reference is closed and names a known variable
// (artist, albumartist, album, year, title, track). It renders the template
// against a fully populated dummy Media, so it cannot catch values that are
// merely empty at import time — those surface as per-album plan warnings.
func ValidateDestTemplate(tmpl string) error {
	probe := metadata.Media{
		Title: "t", Artist: "a", AlbumArtist: "aa", Album: "al", Year: 2000, Track: 1,
	}
	_, err := renderDestTemplate(tmpl, probe)
	return err
}

// aggregateMetadata collapses per-file metadata into a single directory-level
// value per field by taking the most common non-empty value. Album/artist/year
// describe the directory (album) as a whole, so the modal value is the stable
// choice even when a stray file carries different tags.
func aggregateMetadata(items []metadata.Media) metadata.Media {
	modeStr := func(get func(metadata.Media) string) string {
		counts := make(map[string]int)
		for _, it := range items {
			if v := get(it); v != "" {
				counts[v]++
			}
		}
		best, bestN := "", 0
		for v, n := range counts {
			if n > bestN {
				best, bestN = v, n
			}
		}
		return best
	}
	modeInt := func(get func(metadata.Media) int) int {
		counts := make(map[int]int)
		for _, it := range items {
			if v := get(it); v != 0 {
				counts[v]++
			}
		}
		best, bestN := 0, 0
		for v, n := range counts {
			if n > bestN {
				best, bestN = v, n
			}
		}
		return best
	}
	return metadata.Media{
		Artist:      modeStr(func(m metadata.Media) string { return m.Artist }),
		AlbumArtist: modeStr(func(m metadata.Media) string { return m.AlbumArtist }),
		Album:       modeStr(func(m metadata.Media) string { return m.Album }),
		Title:       modeStr(func(m metadata.Media) string { return m.Title }),
		Year:        modeInt(func(m metadata.Media) int { return m.Year }),
		Track:       modeInt(func(m metadata.Media) int { return m.Track }),
	}
}

// importRootPath decides where an imported directory tree is rooted in the
// virtual tree, in precedence order:
//
//  1. an explicit --dest path, if given;
//  2. a per-dir-type template from Config.ImportDest, rendered from the tree's
//     aggregated metadata (falls back to 3 if a template variable is empty);
//  3. the directory's absolute on-disk path (the default), which keeps imports
//     that share a basename from colliding under one root.
func (tie *TieClient) importRootPath(dir, dest, dirType string, meta []metadata.Media) (string, error) {
	if dest != "" {
		return FileURIScheme + "/" + strings.TrimPrefix(filepath.ToSlash(dest), "/"), nil
	}
	// A non-empty template for this dir-type reshapes the root from metadata; an
	// empty template value declares the dir-type as label-only (root at the
	// source path, no reshaping).
	if tmpl, ok := tie.Config.ImportDest[dirType]; ok && tmpl != "" {
		rendered, err := renderDestTemplate(tmpl, aggregateMetadata(meta))
		if err == nil {
			return FileURIScheme + "/" + strings.TrimPrefix(filepath.ToSlash(rendered), "/"), nil
		}
		tie.log().Warn("import destination template not applied, using source path", "error", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return FileURIScheme + filepath.ToSlash(absDir), nil
}

// ImportDir uploads a directory tree to the filehost and mirrors its on-disk
// hierarchy as nested virtual directories. The tree's root is chosen by
// importRootPath (explicit dest, a per-dir-type metadata template, or the
// source's absolute path). Every file is tagged by its content hash and linked
// to its real containing directory's DirUID, so the tree can be browsed by path;
// each file's own media type is detected individually. dirType is the tie-type
// label written on the import root (e.g. "audio-dir" for an album, or any custom
// type); it is a free-form string, so callers can pass built-in or user-defined
// dir-types. tags are applied to every file.
//
// Member order is left implicit: filesystem order already matches the tiedir
// manifest order. An explicit position triple is only written when a user later
// reorders members (future work).
// albumMeta aggregates album-level tags across the audio files of one imported
// directory so the directory node can carry a clean album title, artist, and
// year instead of only its folder name (which often embeds "Artist - Album").
// album stays empty when the files carry different album tags; a mixed artist
// collapses to "Various Artists".
type albumMeta struct {
	album, artist       string
	albumSet, artistSet bool
	albumMixed          bool
	artistMixed         bool
	year                int
}

func (a *albumMeta) add(m metadata.Media) {
	if m.Album != "" {
		if !a.albumSet {
			a.album, a.albumSet = m.Album, true
		} else if a.album != m.Album {
			a.albumMixed = true
		}
	}
	if m.Artist != "" {
		if !a.artistSet {
			a.artist, a.artistSet = m.Artist, true
		} else if a.artist != m.Artist {
			a.artistMixed = true
		}
	}
	if a.year == 0 && m.Year != 0 {
		a.year = m.Year
	}
}

// writeOps adds the resolved album/artist/year triples to a directory node,
// using Set so a re-import replaces rather than accumulates.
func (a *albumMeta) writeOps(batch *api.Batch, uid DirUID) {
	if a.albumSet && !a.albumMixed {
		batch.Set(str(uid), str(TieAlbum), []string{a.album})
	}
	if a.artistSet {
		artist := a.artist
		if a.artistMixed {
			artist = "Various Artists"
		}
		batch.Set(str(uid), str(TieArtist), []string{artist})
	}
	if a.year != 0 {
		batch.Set(str(uid), str(TieYear), []string{strconv.Itoa(a.year)})
	}
}

// ImportDir uploads a directory tree and writes its tree triples into the
// client's scoped collection: one collection for the directory nodes, the file
// rows and the tags, so parent edges cannot point outside it (§3.8). It is a
// plan builder for executeImport (§7.9): upload the tree, describe what came
// back, and let the engine do the tagging.
func (tie *TieClient) ImportDir(dir string, host FileHost, dirType string, tags []string, dest string, forcedArchive TieType) error {
	status := putlib.Upload(host.URL, dir, putlib.PutConfig{Client: HTTPClientFor(host), Store: host.Store})
	if status.ErrorMsg != "" {
		return fmt.Errorf("uploading %s: %s", status.LastItem.Filename, status.ErrorMsg)
	}

	// Each file is opened once here — classify and read its embedded tags
	// together (§5.2 item 3). The results feed both the root-path template
	// ("tie:/%album/%artist") and the per-file tagging below.
	type ingested struct {
		tieType TieType
		media   metadata.Media
	}
	ing := make(map[string]ingested, len(status.UploadedItems))
	allMeta := make([]metadata.Media, 0, len(status.UploadedItems))
	for _, x := range status.UploadedItems {
		if x.MediaType == "inode/directory" {
			continue
		}
		tieType, media := ingestFor(tie, x.Filename)
		ing[x.Filename] = ingested{applyArchiveOverride(tieType, forcedArchive), media}
		allMeta = append(allMeta, media)
	}

	rootPath, err := tie.importRootPath(dir, dest, dirType, allMeta)
	if err != nil {
		return fmt.Errorf("import %s: root path: %w", dir, err)
	}

	p := importPlan{
		Root:         rootPath,
		Tags:         tags,
		DirType:      dirType,
		StampDirType: true,
		// A whole-tree import owns every child of every directory it walked, so
		// anything the tree no longer contains is superseded.
		Reconcile: true,
	}
	for _, x := range status.UploadedItems {
		rel, err := filepath.Rel(dir, x.Filename)
		if err != nil {
			return fmt.Errorf("import %s: %w", x.Filename, err)
		}
		rel = filepath.ToSlash(rel)
		if x.MediaType == "inode/directory" {
			name := path.Base(rel)
			if rel == "." {
				// The import root takes its name from the virtual path.
				name = path.Base(strings.TrimPrefix(rootPath, FileURIScheme))
			}
			p.Dirs = append(p.Dirs, planDir{Rel: rel, Name: name, Size: x.Size, TiedirHash: x.Hash})
			continue
		}
		item := ing[x.Filename]
		p.Items = append(p.Items, planItem{
			Src:       x.Filename,
			Rel:       rel,
			Hash:      x.Hash,
			Size:      x.Size,
			MediaType: x.MediaType,
			TieType:   item.tieType,
			Metadata:  item.media,
		})
	}
	return tie.executeImport(p)
}
