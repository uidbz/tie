package client

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/metadata/tag"
)

// GroupMode selects how PlanAlbumImport clusters audio files into albums.
type GroupMode int

const (
	// GroupAuto groups by directory, then splits directories whose files carry
	// conflicting album tags and merges groups whose tags prove they are the
	// same album (multi-disc sets, scattered rips).
	GroupAuto GroupMode = iota
	// GroupDir groups strictly by directory: each directory containing audio
	// files directly is one album, imported as a faithful tree mirror.
	GroupDir
	// GroupTags ignores the directory layout and clusters by album tags;
	// untagged files fall back to per-directory groups.
	GroupTags
)

// ParseGroupMode converts a CLI flag value ("auto"|"dir"|"tags") to a
// GroupMode; the empty string selects the default (auto).
func ParseGroupMode(s string) (GroupMode, error) {
	switch s {
	case "", "auto":
		return GroupAuto, nil
	case "dir":
		return GroupDir, nil
	case "tags":
		return GroupTags, nil
	}
	return GroupAuto, fmt.Errorf("unknown grouping mode %q (want auto, dir or tags)", s)
}

// AlbumGroup is one album discovered by PlanAlbumImport: the source files, the
// rendered import destination, and human-reviewable warnings.
type AlbumGroup struct {
	// SourceDir is the group's on-disk root: the directory itself for a dir
	// group, the common prefix of the member files for a merged/tag group,
	// the containing directory for an archive group.
	SourceDir string
	// Files, when non-nil, is the explicit audio file list to import (merged,
	// split and tag groups). Nil means the whole SourceDir tree is imported
	// (dir groups), sidecars and subdirectories included.
	Files []string
	// IsArchive marks a single-file group: an archive blob of audio members.
	// It imports as one file into Dest and gets no dir-type stamp — the blob
	// itself carries the audio-archive classification.
	IsArchive bool
	// Dest is the virtual import root without a scheme (e.g.
	// "/music/Miles Davis/1959. Kind of Blue"), rendered from the dir-type's
	// ImportDest template, or the on-disk source path when no template
	// applies. Empty when nothing could be rendered; the group is skipped.
	Dest   string
	Artist string // aggregated: AlbumArtist, falling back to Artist
	Album  string
	Year   int
	Tracks int   // audio file count (1 for archive groups)
	Size   int64 // total member bytes
	// Warnings lists review notes: missing tags, mixed artists without an
	// album-artist tag, destination collisions with other groups.
	Warnings []string

	metas []metadata.Media // member audio metadata, for aggregation
}

// WholeTree reports whether the group imports its SourceDir as a faithful
// tree mirror via ImportDir (sidecars included), rather than an explicit
// audio file list.
func (g AlbumGroup) WholeTree() bool { return g.Files == nil && !g.IsArchive }

// AlbumPlanOptions configures PlanAlbumImport.
type AlbumPlanOptions struct {
	DirType string // ImportDest lookup key, e.g. "audio-dir"
	// Template overrides Config.ImportDest[DirType] when non-empty (the CLI's
	// --dest doubles as an inline template in album mode).
	Template string
	Mode     GroupMode
	// ScanProgress, when non-nil, is called during the library scan with the
	// probed and total file counts (at most once per 1000 files, plus a final
	// call). Libraries on slow or network filesystems take a while to probe.
	ScanProgress func(scanned, total int)
}

// scannedFile is one probed library file: classification, size and (for audio)
// extracted tags.
type scannedFile struct {
	path      string
	size      int64
	isArchive bool // audio archive blob (tie-type audio-archive)
	meta      metadata.Media
}

// PlanAlbumImport scans root for albums and returns the import plan without
// touching the network: which groups import where, with what counts and
// warnings. dirType selects the destination template (Config.ImportDest),
// overridden by opts.Template. Grouping follows opts.Mode (see GroupMode).
//
// An archive of audio members becomes a one-file group (imported as an
// audio-archive blob). A directory whose files carry conflicting album tags
// is split (auto mode), and directories sharing one album identity are merged
// (auto/tags mode); split and merged groups import their audio files only.
// A whole-tree group nested around another group double-parents the nested
// files (legitimate, but flagged in Warnings).
func PlanAlbumImport(cfg Config, root string, opts AlbumPlanOptions) ([]AlbumGroup, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	audio, err := scanAudioFiles(root, opts.ScanProgress)
	if err != nil {
		return nil, err
	}
	groups := groupAlbums(audio, opts.Mode)

	tmpl := opts.Template
	if tmpl == "" {
		tmpl = cfg.ImportDest[opts.DirType]
	}
	finalizePlan(groups, tmpl)
	return groups, nil
}

// finalizePlan renders every group's destination, fills display fields, adds
// collision/nesting warnings, and sorts the plan by destination.
func finalizePlan(groups []AlbumGroup, tmpl string) {
	for i := range groups {
		finalizeAlbumGroup(&groups[i], tmpl)
	}
	warnDestCollisions(groups)
	warnNestedGroups(groups)
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Dest != groups[j].Dest {
			return groups[i].Dest < groups[j].Dest
		}
		return groups[i].SourceDir < groups[j].SourceDir
	})
}

// scanAudioFiles walks root and classifies every file, keeping audio files
// (with their extracted tags) and audio-archive blobs. Classification runs on
// a wide worker pool: it is I/O-bound, and libraries may live on high-latency
// (network) filesystems where many in-flight opens are what buys throughput.
// progress, when non-nil, receives (probed, total) updates.
func scanAudioFiles(root string, progress func(scanned, total int)) ([]scannedFile, error) {
	type pathSize struct {
		path string
		size int64
	}
	var paths []pathSize
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		paths = append(paths, pathSize{p, info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	workers := runtime.NumCPU() * 8
	if workers > 64 {
		workers = 64
	}
	if workers < 8 {
		workers = 8
	}
	jobs := make(chan pathSize)
	var mu sync.Mutex
	var out []scannedFile
	probed := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				t, meta, err := probeMediaFile(j.path)
				mu.Lock()
				probed++
				if progress != nil && probed%1000 == 0 {
					progress(probed, len(paths))
				}
				mu.Unlock()
				if err != nil {
					continue // unreadable files simply don't group
				}
				sf := scannedFile{path: j.path, size: j.size}
				switch t {
				case TieAudioFile:
					sf.meta = meta
				case TieAudioArchive:
					sf.isArchive = true
				default:
					continue
				}
				mu.Lock()
				out = append(out, sf)
				mu.Unlock()
			}
		}()
	}
	for _, j := range paths {
		jobs <- j
	}
	close(jobs)
	wg.Wait()
	if progress != nil {
		progress(len(paths), len(paths))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// probeMediaFile classifies path and, for audio files, extracts embedded tags
// — all over a single open of the file (an important saving on network
// filesystems). The tie-type, metadata (empty for non-audio), and any open or
// read error are returned; a tag parse failure on an audio file degrades to
// empty metadata rather than an error.
func probeMediaFile(path string) (TieType, metadata.Media, error) {
	f, err := os.Open(path)
	if err != nil {
		return TieUnknownFile, metadata.Media{}, err
	}
	defer f.Close()

	t, err := GetTieType(f)
	if err != nil {
		return TieUnknownFile, metadata.Media{}, err
	}
	switch t {
	case TieAudioFile:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return t, metadata.Media{}, nil
		}
		m, err := tag.ReadFrom(f)
		if err != nil {
			return t, metadata.Media{}, nil
		}
		track, _ := m.Track()
		return t, metadata.Media{
			Title:       m.Title(),
			Artist:      m.Artist(),
			AlbumArtist: m.AlbumArtist(),
			Album:       m.Album(),
			Year:        m.Year(),
			Track:       track,
			Duration:    m.Duration().Seconds(),
		}, nil
	case TieArchiveFile:
		// The fd is a seekable ReaderAt: peek at the members to refine the
		// classification (audio-archive etc.) without reopening the file —
		// but only for zip. Zip listing reads the central directory by random
		// access, while rar/7z/iso listings stream (decompress) the whole
		// archive: far too expensive for a bulk scan over possibly huge
		// files on a network filesystem. Non-zip archives stay archive-file
		// in the plan; `import audio-archive` covers them explicitly.
		if isZip, err := hasZipMagic(f); err == nil && isZip {
			if members, err := archivelib.List(f); err == nil {
				t = ArchiveTieType(archivelib.ModalKind(members))
			}
		}
	}
	return t, metadata.Media{}, nil
}

// hasZipMagic reports whether the stream starts with the zip local-file (or
// empty-archive) signature, restoring the read position afterwards.
func hasZipMagic(f io.ReadSeeker) (bool, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	var head [4]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && n == 0 {
		return false, err
	}
	return head[0] == 'P' && head[1] == 'K' && (head[2] == 3 || head[2] == 5), nil
}

// albumKey is the normalized album identity used for merging/splitting: the
// album tag plus the album-level artist (album-artist preferred). The bool is
// false when the file carries no album tag — untagged files never merge.
type albumKey struct {
	artist, album string
}

func albumKeyOf(m metadata.Media) (albumKey, bool) {
	album := normTag(m.Album)
	if album == "" {
		return albumKey{}, false
	}
	artist := m.AlbumArtist
	if artist == "" {
		artist = m.Artist
	}
	return albumKey{artist: normTag(artist), album: album}, true
}

func normTag(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// groupAlbums clusters scanned audio files into album groups following mode.
// Archives always form their own one-file groups.
func groupAlbums(files []scannedFile, mode GroupMode) []AlbumGroup {
	var groups []AlbumGroup
	byDir := map[string][]scannedFile{}
	var dirs []string
	for _, f := range files {
		if f.isArchive {
			groups = append(groups, AlbumGroup{
				SourceDir: filepath.Dir(f.path),
				Files:     []string{f.path},
				IsArchive: true,
				Tracks:    1,
				Size:      f.size,
			})
			continue
		}
		d := filepath.Dir(f.path)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f)
	}
	sort.Strings(dirs)

	switch mode {
	case GroupDir:
		for _, d := range dirs {
			groups = append(groups, dirAlbumGroup(d, byDir[d]))
		}
	case GroupTags:
		groups = append(groups, tagAlbumGroups(byDir, dirs)...)
	default: // GroupAuto
		groups = append(groups, autoAlbumGroups(byDir, dirs)...)
	}
	return groups
}

// dirAlbumGroup is a whole-tree dir group: every file under d imports through
// ImportDir, so Files stays nil (sidecars and subdirectories ride along).
func dirAlbumGroup(d string, members []scannedFile) AlbumGroup {
	g := AlbumGroup{SourceDir: d}
	for _, f := range members {
		g.Tracks++
		g.Size += f.size
		g.metas = append(g.metas, f.meta)
	}
	return g
}

// fileListGroup builds a group over an explicit file list (a split subgroup,
// a merged multi-dir album, or a tag cluster). sourceDir is the common prefix
// of the member paths.
func fileListGroup(members []scannedFile) AlbumGroup {
	g := AlbumGroup{SourceDir: commonDir(memberPaths(members))}
	for _, f := range members {
		g.Files = append(g.Files, f.path)
		g.Tracks++
		g.Size += f.size
		g.metas = append(g.metas, f.meta)
	}
	return g
}

// groupCandidate is a not-yet-final cluster of audio files: either a
// whole-tree dir group or an explicit file list, carrying the album identity
// it may merge under plus any preset destination/warnings.
type groupCandidate struct {
	key      albumKey
	members  []scannedFile
	dir      string // whole-tree group source ("" for file-list candidates)
	dest     string // preset destination (remainder groups: the source path)
	warnings []string
}

// autoAlbumGroups implements the default grouping: directories whose direct
// files carry conflicting album tags are split by tag, and directories
// sharing one album identity merge (multi-disc sets, scattered rips).
func autoAlbumGroups(byDir map[string][]scannedFile, dirs []string) []AlbumGroup {
	byKey := map[albumKey][]int{}
	var candidates []groupCandidate
	add := func(c groupCandidate) {
		idx := len(candidates)
		candidates = append(candidates, c)
		if c.key.album != "" {
			byKey[c.key] = append(byKey[c.key], idx)
		}
	}

	for _, d := range dirs {
		members := byDir[d]
		tagged, untagged := partitionTagged(members)
		if len(tagged) <= 1 {
			// A single (or no) album identity: one whole-tree dir group.
			var key albumKey
			for k := range tagged {
				key = k
			}
			add(groupCandidate{key: key, members: members, dir: d})
			continue
		}
		// Conflicting album tags in one directory: split into one file-list
		// group per identity; untagged files form a remainder group placed at
		// the source path.
		for key, ms := range tagged {
			add(groupCandidate{key: key, members: ms})
		}
		if len(untagged) > 0 {
			add(groupCandidate{
				members:  untagged,
				dest:     d,
				warnings: []string{"tracks without an album tag"},
			})
		}
	}

	// Merge candidates sharing a key across directories; a key held by a
	// single whole-tree candidate stays a dir group.
	var groups []AlbumGroup
	merged := map[int]bool{}
	for _, idxs := range byKey {
		if len(idxs) == 1 {
			continue
		}
		var members []scannedFile
		for _, idx := range idxs {
			members = append(members, candidates[idx].members...)
			merged[idx] = true
		}
		groups = append(groups, fileListGroup(members))
	}
	for idx, c := range candidates {
		if merged[idx] {
			continue
		}
		var g AlbumGroup
		if c.dir != "" {
			g = dirAlbumGroup(c.dir, c.members)
		} else {
			g = fileListGroup(c.members)
		}
		g.Dest = c.dest
		g.Warnings = append(g.Warnings, c.warnings...)
		groups = append(groups, g)
	}
	return groups
}

// tagAlbumGroups implements tag-driven clustering: every tagged audio file
// joins its album identity's group regardless of location; untagged files
// form per-directory groups placed at their source paths. No absorption: the
// directory layout is ignored entirely.
func tagAlbumGroups(byDir map[string][]scannedFile, dirs []string) []AlbumGroup {
	byKey := map[albumKey][]scannedFile{}
	var untaggedByDir = map[string][]scannedFile{}
	for _, d := range dirs {
		tagged, untagged := partitionTagged(byDir[d])
		for key, ms := range tagged {
			byKey[key] = append(byKey[key], ms...)
		}
		if len(untagged) > 0 {
			untaggedByDir[d] = untagged
		}
	}
	var groups []AlbumGroup
	for _, members := range byKey {
		groups = append(groups, fileListGroup(members))
	}
	for d, members := range untaggedByDir {
		g := fileListGroup(members)
		g.Warnings = append(g.Warnings, "tracks without an album tag")
		g.Dest = d
		groups = append(groups, g)
	}
	return groups
}

// partitionTagged splits members by album identity: tagged files cluster
// under their key, untagged files collect separately.
func partitionTagged(members []scannedFile) (tagged map[albumKey][]scannedFile, untagged []scannedFile) {
	tagged = map[albumKey][]scannedFile{}
	for _, f := range members {
		if key, ok := albumKeyOf(f.meta); ok {
			tagged[key] = append(tagged[key], f)
		} else {
			untagged = append(untagged, f)
		}
	}
	return tagged, untagged
}

// finalizeAlbumGroup fills the group's aggregated display fields, renders its
// destination, and adds tag-related warnings.
func finalizeAlbumGroup(g *AlbumGroup, tmpl string) {
	if g.IsArchive {
		// Archives carry no readable tags at this level; they import next to
		// their on-disk location and classify themselves as audio-archive.
		g.Artist = ""
		g.Album = strings.TrimSuffix(filepath.Base(g.Files[0]), filepath.Ext(g.Files[0]))
		g.Dest = g.SourceDir
		return
	}
	agg := aggregateMetadata(g.metas)
	g.Artist = agg.AlbumArtist
	if g.Artist == "" {
		g.Artist = agg.Artist
	}
	g.Album = agg.Album
	g.Year = agg.Year
	if g.Dest == "" {
		if tmpl != "" {
			if rendered, err := renderDestTemplate(tmpl, agg); err == nil {
				g.Dest = rendered
			} else {
				g.Warnings = append(g.Warnings, "template not applied: "+err.Error())
			}
		}
		if g.Dest == "" {
			switch {
			case g.WholeTree():
				// Directory identity: mirror the source tree (rule 3).
				g.Dest = g.SourceDir
			case agg.Album != "":
				// A tag cluster has no directory identity; falling back to
				// its mere common directory would dump every template-less
				// cluster of a loose library into the same root. Use the
				// album tag as the leaf instead.
				g.Dest = filepath.Join(g.SourceDir, sanitizePathSegment(agg.Album))
			default:
				g.Warnings = append(g.Warnings, "no destination: template did not render and no album tag")
			}
		}
	}
	if agg.Album == "" {
		g.Warnings = append(g.Warnings, "no album tag")
	}
	if agg.Artist == "" {
		g.Warnings = append(g.Warnings, "no artist tag")
	} else if agg.AlbumArtist == "" && mixedArtists(g.metas) {
		g.Warnings = append(g.Warnings, "mixed artists, no album-artist tag")
	}
}

// mixedArtists reports whether the members carry more than one distinct
// artist tag.
func mixedArtists(metas []metadata.Media) bool {
	seen := map[string]bool{}
	for _, m := range metas {
		if a := normTag(m.Artist); a != "" {
			seen[a] = true
			if len(seen) > 1 {
				return true
			}
		}
	}
	return false
}

// warnNestedGroups annotates groups nested inside a whole-tree group: the
// tree import mirrors every file below it, so a nested album's files are
// parented twice — under its own destination and inside the tree's mirror.
// That is legitimate (a file may live in several virtual dirs) but usually
// not intended, so the plan surfaces it.
func warnNestedGroups(groups []AlbumGroup) {
	sep := string(os.PathSeparator)
	for i := range groups {
		if !groups[i].WholeTree() {
			continue
		}
		root := groups[i].SourceDir + sep
		for j := range groups {
			if i == j {
				continue
			}
			nested := strings.HasPrefix(groups[j].SourceDir, root)
			if !nested {
				for _, f := range groups[j].Files {
					if strings.HasPrefix(f, root) {
						nested = true
						break
					}
				}
			}
			if nested {
				groups[j].Warnings = append(groups[j].Warnings,
					"nested inside "+groups[i].SourceDir+" — files also appear in that tree import")
			}
		}
	}
}

// warnDestCollisions annotates album groups whose rendered destination is
// shared with another album group — importing both would merge their files
// into one virtual directory. Archive groups are exempt: they import as
// single files, and files sharing a containing directory is the norm.
func warnDestCollisions(groups []AlbumGroup) {
	count := map[string]int{}
	for _, g := range groups {
		if g.Dest != "" && !g.IsArchive {
			count[g.Dest]++
		}
	}
	for i := range groups {
		if groups[i].IsArchive {
			continue
		}
		if n := count[groups[i].Dest]; n > 1 {
			groups[i].Warnings = append(groups[i].Warnings,
				fmt.Sprintf("destination shared with %d groups", n))
		}
	}
}

// commonDir returns the deepest directory containing every path.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir := filepath.Dir(paths[0])
	sep := string(os.PathSeparator)
	for _, p := range paths[1:] {
		for dir != "." && dir != sep && !strings.HasPrefix(p, dir+sep) {
			dir = filepath.Dir(dir)
		}
	}
	return dir
}

func memberPaths(members []scannedFile) []string {
	paths := make([]string, 0, len(members))
	for _, f := range members {
		paths = append(paths, f.path)
	}
	sort.Strings(paths)
	return paths
}

// --- execution ---

// AlbumImportOptions configures ImportAlbums.
type AlbumImportOptions struct {
	Host          FileHost
	Collection    string
	DirType       string   // label stamped on each album root, e.g. "audio-dir"
	Tags          []string // extra tags applied to every imported file
	ForcedArchive TieType  // archive classification override (usually zero)
	// Progress, when non-nil, is called before each group's import with its
	// index and the total group count.
	Progress func(done, total int, g AlbumGroup)
}

// AlbumImportResult pairs a planned group with its import outcome.
type AlbumImportResult struct {
	Group AlbumGroup
	Err   error
}

// ImportAlbums executes a plan from PlanAlbumImport, importing group by group
// and collecting per-group errors (one failure does not abort the rest).
// Groups without a destination are recorded as errors.
func (tie *TieClient) ImportAlbums(plan []AlbumGroup, opts AlbumImportOptions) []AlbumImportResult {
	results := make([]AlbumImportResult, 0, len(plan))
	for i, g := range plan {
		if opts.Progress != nil {
			opts.Progress(i, len(plan), g)
		}
		err := tie.importAlbum(g, opts)
		if err != nil {
			err = fmt.Errorf("import album %s: %w", g.Dest, err)
		}
		results = append(results, AlbumImportResult{Group: g, Err: err})
	}
	return results
}

func (tie *TieClient) importAlbum(g AlbumGroup, opts AlbumImportOptions) error {
	if g.Dest == "" {
		return errors.New("no destination: no destination template rendered")
	}
	switch {
	case g.IsArchive:
		// A single archive blob into the destination directory; no dir-type
		// stamp — the blob itself carries audio-archive.
		uid, err := tie.MkTieDirAll(FileURIScheme + g.Dest)
		if err != nil {
			return err
		}
		return tie.ImportFile(g.Files[0], opts.Host, opts.Collection, opts.Tags, uid, opts.ForcedArchive)
	case g.WholeTree():
		return tie.ImportDir(g.SourceDir, opts.Host, opts.Collection, opts.DirType, opts.Tags, g.Dest, opts.ForcedArchive)
	default:
		return tie.importAlbumFiles(g, opts)
	}
}

// importAlbumFiles imports an explicit audio file list (a merged, split or
// tag-clustered group) under the group's destination, mirroring ImportDir's
// batched tagging: subdirectories below SourceDir are preserved, directory
// nodes get name/album aggregates, and the root is stamped with the dir-type.
//
// Unlike ImportDir it is purely additive — no reconciliation — because the
// file list is a subset view: versioning away children this group did not
// place would destroy files belonging to other albums sharing the directory.
func (tie *TieClient) importAlbumFiles(g AlbumGroup, opts AlbumImportOptions) error {
	rootUID, err := tie.MkTieDirAll(FileURIScheme + g.Dest)
	if err != nil {
		return err
	}
	const flushThreshold = 1000
	batch := tie.NewBatchIn(opts.Collection)
	flush := func() error {
		if len(batch.Ops) == 0 {
			return nil
		}
		if _, err := tie.Batch(batch); err != nil {
			return err
		}
		batch = tie.NewBatchIn(opts.Collection)
		return nil
	}

	dirUIDs := map[string]DirUID{".": rootUID}
	dirAlbums := map[string]*albumMeta{".": {}}
	parentFor := func(rel string) (DirUID, error) {
		d := filepath.Dir(rel)
		// Ensure d and every missing ancestor below the root exist as tagged
		// directory nodes — a file at "CD1/disc 2/x.flac" must not leave the
		// intermediate "CD1" node anonymous.
		var chain []string
		for cur := d; cur != "." && cur != ""; cur = filepath.Dir(cur) {
			if _, ok := dirUIDs[cur]; !ok {
				chain = append(chain, cur)
			}
		}
		for i := len(chain) - 1; i >= 0; i-- {
			cur := chain[i]
			uid, err := tie.MkTieDirAll(FileURIScheme + g.Dest + "/" + filepath.ToSlash(cur))
			if err != nil {
				return "", err
			}
			dirUIDs[cur] = uid
			dirAlbums[cur] = &albumMeta{}
			appendTagDirOps(batch, uid, filepath.Base(cur), 0, opts.Tags)
		}
		return dirUIDs[d], nil
	}

	var total int
	for _, f := range g.Files {
		rel, err := filepath.Rel(g.SourceDir, f)
		if err != nil {
			return err
		}
		parent, err := parentFor(rel)
		if err != nil {
			return err
		}
		status := putlib.Upload(opts.Host.URL, f, putlib.PutConfig{Client: HTTPClientFor(opts.Host), Store: opts.Host.Store})
		if status.ErrorMsg != "" {
			return fmt.Errorf("upload %s: %s", f, status.ErrorMsg)
		}
		x := status.LastItem
		fileType, err := GetTieTypeFromPath(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: cannot classify (%v); tagging as %s\n", f, err, TieUnknownFile)
			fileType = TieUnknownFile
		}
		fileType = applyArchiveOverride(fileType, opts.ForcedArchive)
		meta := ExtractMediaMetadata(f)
		info := TagInfo{
			Hash:      x.Hash,
			File:      f,
			Size:      x.Size,
			MediaType: x.MediaType,
			Directory: parent,
			TieType:   fileType,
			Tags:      opts.Tags,
			Metadata:  meta,
		}
		appendTagOps(batch, info)
		dirAlbums[filepath.Dir(rel)].add(meta)
		total += x.Size
		if len(batch.Ops) >= flushThreshold {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	for rel, uid := range dirUIDs {
		if rel == "." {
			appendTagDirOps(batch, uid, filepath.Base(g.Dest), total, opts.Tags)
		}
		dirAlbums[rel].writeOps(batch, uid)
	}
	if err := flush(); err != nil {
		return err
	}
	return tie.SetDirType(rootUID, opts.DirType)
}
