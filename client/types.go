package client

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/uidbz/tie/metadata"
)

// The client's vocabulary: the dir-type and property enums, DirUID, and the
// shapes the rest of the package passes around — TagInfo, the directory tree
// read shapes, TaggedFile, MediaRelation. Split out of tag.go (§7.11).

//go:generate stringer -type=TieType -linecomment
//go:generate stringer -type=TieProperty -linecomment

type TieType int

type TieProperty int

type TieCategory int

type DirUID string

const (
	TieUnknownFile     TieType = iota // unknown-file
	TieImageFile                      // image-file
	TieAudioFile                      // audio-file
	TieVideoFile                      // video-file
	TieDocumentFile                   // document-file
	TieArchiveFile                    // archive-file
	TieImageDir                       // image-dir
	TieAudioDir                       // audio-dir
	TieVideoDir                       // video-dir
	TieDocumentDir                    // document-dir
	TieImageArchive                   // image-archive
	TieAudioArchive                   // audio-archive
	TieVideoArchive                   // video-archive
	TieDocumentArchive                // document-archive
	TieDirectory                      // directory
	TieFile                           // file
)

const (
	TieUid          TieProperty = iota // tie-uid
	TieFilename                        // filename
	TieFilesize                        // filesize
	TieName                            // name
	TieMediaType                       // media-type
	TieFileHost                        // filehost
	TieTag                             // tag
	TiePath                            // path
	TieParent                          // parent
	TieTags                            // tags
	TieTagDate                         // tag-date
	TieCollection                      // collection
	TieTypeProperty                    // tie-type
	TieAll                             // all
	TieTitle                           // title
	TieArtist                          // artist
	TieAlbum                           // album
	TieYear                            // year
	TieTrack                           // track
	TieTiedirHash                      // tiedir-hash
	TieDirUID                          // dir-uid
	TieVersionOf                       // version-of
	TieVersionDate                     // version-date
	TieFavorite                        // favorite
)

// FileURIScheme prefixes the virtual path stored in every (uid, "path", …)
// triple, namespacing the tag/path tree. It is a private URI scheme conforming
// to RFC 3986 generic syntax ("tie" is a valid scheme name; "tie:/a/b" is a
// well-formed URI with no authority) — deliberately NOT the RFC 8089 "file:"
// scheme, since these are tie-internal tree nodes, not local-filesystem paths.
// The value is persisted, so changing it requires migrating existing path
// triples (see cmd/tie migration notes / test-env/migrate-scheme.sh).
const (
	FileURIScheme string = "tie:"
)

// tagDateFormat is the layout for the single-valued tag-date (last import time).
// It carries microsecond precision so several re-imports within the same wall
// second still order correctly for version retention; older second-resolution
// values (written before this) still parse, since the fractional part is
// optional in time.Parse.
const tagDateFormat = "2006-01-02 15:04:05.000000"

func (d DirUID) String() string {
	return string(d)
}

func str(t fmt.Stringer) string {
	return t.String()
}

// tieTypeByName inverts TieType.String() so the name->value mapping stays in
// sync with the stringer output instead of being hand-maintained.
var tieTypeByName = func() map[string]TieType {
	m := make(map[string]TieType)
	for t := TieUnknownFile; t <= TieFile; t++ {
		m[t.String()] = t
	}
	return m
}()

func StringToTieType(t string) TieType {
	if tt, ok := tieTypeByName[t]; ok {
		return tt
	}
	return TieUnknownFile
}

func SliceToTieType(types []string) (t []TieType) {
	for _, x := range types {
		t = append(t, StringToTieType(x))
	}
	return t
}

type TagInfo struct {
	Hash      string
	File      string
	Size      int
	MediaType string
	TieType   TieType
	Tags      []string
	Directory DirUID
	IsDir     bool
	Metadata  metadata.Media
}

// applyArchiveOverride forces an archive file's tie-type to forced when forced
// names an archive type and the sniffed type is itself an archive. Non-archive
// files keep their auto-detected type. This backs `import <archive-type>`, which
// forces the classification of zips (e.g. an album zip that auto-detects as
// image-archive because scans dominate) without disturbing other files.
func applyArchiveOverride(sniffed, forced TieType) TieType {
	if IsArchiveType(forced) && IsArchiveType(sniffed) {
		return forced
	}
	return sniffed
}

// ErrDirExists reports that the directory a caller asked to create was already
// there. MkTieDir returns it together with the existing UID, so a caller that
// wants "create if absent" tests errors.Is and carries on. Before it, control
// flow hinged on strings.HasPrefix(err.Error(), "Root dir already exists") —
// wording changes silently inverted the check (§4.10).
var ErrDirExists = errors.New("directory exists")

type Directory struct {
	Paths      []string
	Uid        DirUID
	SubDirs    []SubDirectory
	Files      []File
	Archives   []ArchiveEntry
	ParentUIDs []DirUID
}

type SubDirectory struct {
	Paths    []string
	Uid      DirUID
	DirTypes []TieType
}

type File struct {
	Filename  string
	Uid       string
	TieType   TieType
	MediaType string
	Size      int
	// TagDate is the file's last import time, parsed from its (hash,"tag-date")
	// triple. Zero when no date is recorded. The FUSE mount surfaces it as the
	// file's mtime.
	TagDate time.Time
}

// ArchiveEntry is a directory child that is a single archive blob (a zip)
// carrying an archive tie-type. Unlike a SubDirectory it has no graph
// child-edges: its "directory" contents live inside the blob and are produced
// by expanding it at read time (see io/archivelib). Uid is the blob's content
// hash, which is the expansion key — never pass it to ReadTieDir.
type ArchiveEntry struct {
	Filename string
	Hash     string
	TieType  TieType
	Size     int
	TagDate  time.Time
}

// archiveTypes are the tie-types that mark a blob as a browsable archive, most
// specific first.
var archiveTypes = []TieType{TieImageArchive, TieAudioArchive, TieVideoArchive, TieDocumentArchive, TieArchiveFile}

// IsArchiveType reports whether t marks a blob that should be expanded as a
// virtual directory of members.
func IsArchiveType(t TieType) bool {
	return slices.Contains(archiveTypes, t)
}

func containsArchiveType(types []string) bool {
	for _, t := range archiveTypes {
		if slices.Contains(types, str(t)) {
			return true
		}
	}
	return false
}

func archiveTypeOf(types []string) TieType {
	for _, t := range archiveTypes {
		if slices.Contains(types, str(t)) {
			return t
		}
	}
	return TieArchiveFile
}

// TaggedFile is one entry in a tag-derived virtual directory: its content hash
// (used to fetch bytes, or a tiedir blob, from the filehost), display filename,
// size, and whether it is a directory. A tagged directory's hash points at an
// immutable tiedir blob, so it can be expanded with the content-addressed tree.
type TaggedFile struct {
	Hash     string
	Filename string
	Size     int
	IsDir    bool
	// IsArchive marks a single archive blob (a zip) that is browsable as a
	// virtual directory of its members. It is a file structurally (IsDir is
	// false), but consumers should expand it at read time via io/archivelib
	// rather than treating it as plain downloadable content. TieType carries the
	// specific archive type when IsArchive is set.
	IsArchive bool
	TieType   TieType
}

// MediaRelation is a named, directed association from one media item to another,
// both identified by content hash (e.g. {hashA, "sampled-from", hashB}).
type MediaRelation struct {
	FromHash string
	Relation string
	ToHash   string
}
