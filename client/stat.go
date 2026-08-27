package client

// stat.go aggregates everything known about a single item — a file, an archive,
// or a directory — into one plain-data StatInfo value. It is the shared backend
// for `tie stat` (CLI) and tie-fm's Properties dialog (GUI): the client package
// resolves and gathers, each front-end only formats. StatInfo therefore holds
// no CLI- or GUI-specific types.

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StatKind is the coarse classification of a stat target.
type StatKind string

const (
	StatFile      StatKind = "file"
	StatDirectory StatKind = "directory"
	StatArchive   StatKind = "archive"
)

// StatInfo is the full metadata summary for one item. Filehost-derived and
// history fields are populated only when the matching StatOptions flag is set;
// everything else comes from the triple store in a single Get (plus one child
// query for directories).
type StatInfo struct {
	Key        string   // subject: content hash (file/archive) or DirUID (directory)
	Kind       StatKind //
	TieType    TieType  // full classification (e.g. audio-file, image-archive)
	Filename   string   //
	Name       string   // filename without extension
	MediaType  string   // MIME, e.g. "image/png"
	Size       int64    // recorded filesize triple (0 when absent)
	Tags       []string //
	TagDate    time.Time
	Meta       map[string]string // media metadata: title/artist/album/year/track when present
	Paths      []string          // stored virtual paths (directories); reconstructed path for files
	ParentUIDs []string

	// Directory only (from the child query):
	SubDirCount  int
	FileCount    int
	ArchiveCount int

	// TotalSize is the recursive download size; filled only when opts.Recursive.
	TotalSize int64

	// Versions holds superseded versions, newest first; filled only when
	// opts.Versions and the item is a file with a resolvable path.
	Versions []VersionInfo

	// Blob* are filled only when opts.CheckBlob (a filehost HEAD).
	BlobChecked bool
	BlobExists  bool
	BlobSize    int64

	// Attributes is the raw relation->values map from the store — an escape hatch
	// so a front-end can surface any triple StatInfo does not model explicitly.
	Attributes map[string][]string
}

// StatOptions controls the optional, network-touching parts of a stat. The zero
// value is fully offline (triple store only).
type StatOptions struct {
	Recursive bool // compute TotalSize via a filehost (DownloadSize)
	CheckBlob bool // HEAD the filehost for blob existence + on-disk size
	Versions  bool // populate version history when resolvable
}

// mediaMetaProps are the optional media-metadata relations surfaced in Meta.
var mediaMetaProps = []TieProperty{TieTitle, TieArtist, TieAlbum, TieYear, TieTrack}

// Stat gathers metadata for a subject key (a content hash for files/archives, or
// a DirUID for directories). tie-fm passes the key it already holds on a
// selected entry. Returns ErrNotFound when the key carries no triples (e.g. a
// raw tiedir-snapshot blob hash, whose triples live on its DirUID).
func (tc *TieClient) Stat(subject string, opts StatOptions) (StatInfo, error) {
	return tc.stat(subject, "", opts)
}

// StatPath resolves a virtual path (a directory path, or a file leaf) to its
// subject key and then stats it. Because the path is known, version history is
// always resolvable here when requested.
func (tc *TieClient) StatPath(path string, opts StatOptions) (StatInfo, error) {
	// A path naming a directory resolves directly to its DirUID.
	if uid, err := tc.DirUIDFromPath(path); err != nil {
		return StatInfo{}, err
	} else if uid != "" {
		return tc.stat(string(uid), normalizePath(path), opts)
	}

	// Otherwise treat it as a file leaf: parent dir + filename -> content hash.
	parentUID, name, err := tc.resolveFileLoc(path)
	if err != nil {
		return StatInfo{}, err
	}
	dir, err := ReadTieDir(tc, parentUID)
	if err != nil {
		return StatInfo{}, err
	}
	for _, f := range dir.Files {
		if f.Filename == name {
			return tc.stat(f.Uid, normalizePath(path), opts)
		}
	}
	for _, a := range dir.Archives {
		if a.Filename == name {
			return tc.stat(a.Hash, normalizePath(path), opts)
		}
	}
	return StatInfo{}, ErrNotFound
}

// stat is the shared core. knownPath is the resolved virtual path when the entry
// was reached via StatPath (used for version lookup); it is "" for a bare-key
// Stat, in which case a file's path is reconstructed from its parent.
func (tc *TieClient) stat(subject, knownPath string, opts StatOptions) (StatInfo, error) {
	row, err := tc.Get(subject)
	if err != nil {
		if errors.Is(err, ErrNotFound) && opts.CheckBlob {
			// No triples, but the caller wants to know if the blob is on disk.
			info := StatInfo{Key: subject, Kind: StatFile}
			tc.fillBlob(&info, subject)
			return info, nil
		}
		return StatInfo{}, err
	}

	types := RowValues(row, str(TieTypeProperty))
	size, _ := strconv.ParseInt(RowFirst(row, str(TieFilesize)), 10, 64)
	info := StatInfo{
		Key:        subject,
		Filename:   RowFirst(row, str(TieFilename)),
		Name:       RowFirst(row, str(TieName)),
		MediaType:  RowFirst(row, str(TieMediaType)),
		Size:       size,
		Tags:       sortedCopy(RowValues(row, str(TieTag))),
		TagDate:    parseTagDate(RowFirst(row, str(TieTagDate))),
		ParentUIDs: RowValues(row, str(TieParent)),
		Paths:      RowValues(row, str(TiePath)),
		Attributes: row.Attributes,
	}
	for _, p := range mediaMetaProps {
		if v := RowFirst(row, str(p)); v != "" {
			if info.Meta == nil {
				info.Meta = make(map[string]string)
			}
			info.Meta[str(p)] = v
		}
	}

	switch {
	case RowHas(row, str(TieTypeProperty), str(TieDirectory)):
		info.Kind = StatDirectory
		info.TieType = TieDirectory
		dir, err := ReadTieDir(tc, DirUID(subject))
		if err != nil {
			return StatInfo{}, err
		}
		info.SubDirCount = len(dir.SubDirs)
		info.FileCount = len(dir.Files)
		info.ArchiveCount = len(dir.Archives)
		if len(info.Paths) == 0 {
			info.Paths = dir.Paths
		}
	case containsArchiveType(types):
		info.Kind = StatArchive
		info.TieType = archiveTypeOf(types)
	default:
		info.Kind = StatFile
		info.TieType = StringToTieType(strings.Join(types, ", "))
	}

	if opts.Recursive {
		if info.Kind == StatDirectory {
			// A DirUID is not a filehost blob, so it cannot be sized via the
			// content-addressed DownloadSize path. Walk the live tag-derived tree
			// and sum recorded filesizes instead — triples only, no blob fetches.
			seen := map[DirUID]bool{}
			info.TotalSize = tc.dirTotalSize(DirUID(subject), seen, 0)
		} else if host, err := tc.ResolveHost(""); err == nil {
			if total, err := DownloadSize(host, subject); err == nil {
				info.TotalSize = total
			}
		}
	}

	if opts.CheckBlob && info.Kind != StatDirectory {
		tc.fillBlob(&info, subject)
	}

	if opts.Versions && info.Kind != StatDirectory {
		if path := knownPath; path != "" || info.Filename != "" {
			if path == "" {
				path = tc.reconstructPath(info)
			}
			if path != "" {
				if vers, err := tc.ListVersions("", path); err == nil {
					info.Versions = vers
				}
			}
		}
	}

	return info, nil
}

// statMaxDirDepth caps directory recursion in dirTotalSize as a backstop against
// a pathological (or maliciously constructed) parent cycle the seen-set misses.
const statMaxDirDepth = 100

// dirTotalSize sums the recorded filesizes of every file and archive reachable
// under uid, recursing into subdirectories. It reads only triples (via
// ReadTieDir) — no filehost round-trips. The seen set guards against parent
// cycles so a cyclic graph is counted once, not infinitely; a read error on any
// subtree contributes 0 rather than failing the whole stat.
func (tc *TieClient) dirTotalSize(uid DirUID, seen map[DirUID]bool, depth int) int64 {
	if depth > statMaxDirDepth || seen[uid] {
		return 0
	}
	seen[uid] = true

	dir, err := ReadTieDir(tc, uid)
	if err != nil {
		return 0
	}
	var total int64
	for _, f := range dir.Files {
		total += int64(f.Size)
	}
	for _, a := range dir.Archives {
		total += int64(a.Size)
	}
	for _, sub := range dir.SubDirs {
		total += tc.dirTotalSize(sub.Uid, seen, depth+1)
	}
	return total
}

// fillBlob HEADs the filehost for subject and records existence + on-disk size.
// A filehost error is swallowed (BlobChecked stays false) so a stat never fails
// solely because the filehost is unreachable.
func (tc *TieClient) fillBlob(info *StatInfo, subject string) {
	exists, size, err := tc.StatBlob(subject)
	if err != nil {
		return
	}
	info.BlobChecked = true
	info.BlobExists = exists
	info.BlobSize = size
}

// reconstructPath builds a file's virtual path from its first parent DirUID's
// stored path plus its filename, so version history is resolvable from a bare
// content hash. Returns "" when the parent path cannot be resolved.
func (tc *TieClient) reconstructPath(info StatInfo) string {
	if info.Filename == "" || len(info.ParentUIDs) == 0 {
		return ""
	}
	parent, err := tc.Get(info.ParentUIDs[0])
	if err != nil {
		return ""
	}
	base := normalizePath(RowFirst(parent, str(TiePath)))
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + info.Filename
}

// normalizePath strips the tie: scheme prefix so a value is comparable to the
// paths ListVersions/DirUIDFromPath expect (they re-add the scheme themselves).
func normalizePath(p string) string {
	return strings.TrimPrefix(p, FileURIScheme)
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}
