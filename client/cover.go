package client

// cover.go resolves collection artwork.
//
// A collection (audio-dir, image-dir, playlist, …) keeps two artwork
// relations:
//
//	(uid, "cover",       <imageHash>)  the chosen source image
//	(uid, "cover-source", "file:cover.jpg" | "embedded:01.flac" | …)
//
// "thumbnail" remains the small, display-ready JPEG cached by clients. The
// split matters: the source image can be a full-size scan or an embedded
// picture, while thumbnail is what every cover wall should fetch.
//
// Resolution is deliberately shared by every front end (CLI, tie-fm,
// tie-audio): candidate discovery, ranking and the "last resort" rules must
// not drift between the apps. This file owns the local half (files, embedded
// pictures, archive members). Online artwork stays in tie-fm's MusicBrainz
// integration.

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/metadata/tag"
)

// ErrNoCover means the collection has no usable artwork from the requested
// sources. A network or filehost failure is returned as another error, so
// callers can cache "no cover" without caching a transient outage.
var ErrNoCover = errors.New("no cover")

// CoverConfidence expresses how likely a candidate is to be the front cover.
// Low candidates are still returned: clients show them only when nothing
// better exists.
type CoverConfidence int

const (
	CoverLow CoverConfidence = iota
	CoverMedium
	CoverHigh
)

// String returns the stable name used by CLI/JSON output.
func (c CoverConfidence) String() string {
	switch c {
	case CoverHigh:
		return "high"
	case CoverMedium:
		return "medium"
	default:
		return "low"
	}
}

// CoverCandidate is one image that could be a collection's artwork.
type CoverCandidate struct {
	Hash     string // content hash of the image blob
	Filename string // basename
	Path     string // slash path relative to the collection root ("" = root)
	Depth    int    // directory depth below the root (0 = root)
	Score    int
	// Confidence follows the filename's evidence, not the path: a
	// low-confidence name in a covers/ folder is still a back/scan image.
	Confidence CoverConfidence
}

// CoverSearchOptions controls local candidate discovery.
type CoverSearchOptions struct {
	// IncludeParent also looks one directory up (disc folders such as CD1/
	// under an album root, or a "flac" subfolder next to the scans).
	IncludeParent bool
	// MaxDepth bounds the subdirectory walk. Zero selects the default (2).
	MaxDepth int
}

// CoverOptions controls ResolveCover.
type CoverOptions struct {
	// Host fetches track/archive blobs for embedded and archive artwork.
	// Empty resolves the client's default host.
	Host FileHost
	// IncludeParent includes parent-folder artwork (conservative: the
	// parent must not be a collection and must look like this album's
	// parent, not a shared library root).
	IncludeParent bool
	// IncludeEmbedded probes the first tracks for embedded pictures.
	IncludeEmbedded bool
	// IncludeArchives extracts artwork from audio-archive blobs.
	IncludeArchives bool
	// MaxDepth bounds the subdirectory walk. Zero selects the default (2).
	MaxDepth int
	// DryRun finds and uploads nothing, but returns the hash/source that
	// would be recorded. Extracted pictures are hashed, not uploaded.
	DryRun bool
}

// CoverResult is the outcome of ResolveCover.
type CoverResult struct {
	Hash       string
	Source     string // "file", "embedded", "archive"
	Path       string // e.g. "artwork/front.jpg" or "01 - track.flac"
	Score      int
	Confidence CoverConfidence
	// Data is set when the resolver already extracted the bytes (embedded
	// or archive artwork). File candidates return only Hash.
	Data []byte
}

// CoverNameScore ranks one image filename as album artwork. Values:
//
//	100  cover/folder/front/albumart exact names
//	 90  a front/cover/folder token anywhere ("(front)", "00-…-front")
//	 80  WMP Large cache names / front+back composites
//	 60  neutral names ("Artist - Album.jpg")
//	 50  numbered scans
//	 40  WMP small cache names
//	 10  back/inside/inlay/booklet/cd/scan/proof/…
//
// Anything below 50 is a last resort.
func CoverNameScore(name string) int {
	stem := strings.ToLower(strings.TrimSuffix(pathpkg.Base(name), pathpkg.Ext(name)))
	if stem == "" {
		return 10
	}
	toks := coverTokens(stem)
	hasFront := coverHasFrontToken(toks)
	hasFrontWord := coverHasWord(toks, "front") || coverHasPrefix(toks, "front")
	hasBack := coverHasWord(toks, "back") || coverHasWord(toks, "rear") ||
		coverHasPrefix(toks, "back") || coverHasPrefix(toks, "rear")
	hasBad := coverHasBadToken(toks)

	score := 60
	exact := false
	switch stem {
	case "cover", "folder", "front", "frontcover", "coverart",
		"cover art", "cover-front", "front-cover", "folderart",
		"albumart", "album art", "artwork", "album":
		score = 100
		exact = true
	}
	if hasFront && score < 90 {
		score = 90
	}
	// Windows Media Player caches: the Large variant is useful, the Small
	// one is a 200px postage stamp.
	if coverWMPLarge(stem) && !exact {
		score = 80
	}
	if coverWMPSmall(stem) {
		score = 40
	}
	if coverWMPAlbumCache(stem) {
		if score < 70 {
			score = 70
		}
	}
	if numberedCoverName(stem) {
		score = 50
	}
	if hasBack {
		if hasFrontWord {
			// "front+back", "back_front": a composite showing the front.
			score = 80
		} else {
			score = 10
		}
	}
	if hasBad && !(hasBack && hasFrontWord) {
		score = 10
	}
	return score
}

// coverConfidence maps a filename score to confidence. The threshold is
// intentionally about the name, not the path.
func coverConfidence(score int) CoverConfidence {
	switch {
	case score >= 90:
		return CoverHigh
	case score >= 50:
		return CoverMedium
	default:
		return CoverLow
	}
}

// coverUserScore is the total ranking of a candidate: filename evidence plus
// its location. Root art beats the same name buried in a scans folder; a
// direct root image beats a small WMP cache in .mediaartlocal.
func coverUserScore(name, rel string) int {
	return CoverNameScore(name) + coverPathScore(rel)
}

// coverPathScore rewards artwork at the album root and in the conventional
// artwork folders, and keeps .mediaartlocal as a fallback.
func coverPathScore(rel string) int {
	rel = strings.TrimPrefix(pathpkg.Clean(strings.ReplaceAll(rel, "\\", "/")), "./")
	if rel == "" || rel == "." {
		return 50
	}
	if strings.HasPrefix(rel, "../") {
		rest := strings.TrimPrefix(rel, "../")
		first := strings.SplitN(rest, "/", 2)[0]
		if isArtDirName(first) {
			return 20
		}
		return 10
	}
	if !strings.Contains(rel, "/") {
		return 50 // the image sits directly in the album root
	}
	first := strings.SplitN(rel, "/", 2)[0]
	if strings.EqualFold(first, ".mediaartlocal") {
		return 5
	}
	if isArtDirName(first) {
		return 25
	}
	return 15
}

var artDirNames = map[string]bool{
	"art": true, "artworks": true, "artwork": true, "cover": true,
	"covers": true, "covers lp": true, "cover art": true, "scan": true,
	"scans": true, "booklet": true, "booklets": true, "extras": true,
	".mediaartlocal": true, "artwork (complete)": true,
}

func isArtDirName(name string) bool {
	return artDirNames[strings.ToLower(strings.TrimSpace(name))]
}

var (
	coverBadWords = map[string]bool{
		"back": true, "rear": true, "inside": true, "inlay": true,
		"booklet": true, "book": true, "cd": true, "disc": true, "disk": true,
		"dvd": true, "tray": true, "matrix": true, "label": true, "obi": true,
		"spine": true, "side": true, "scan": true, "proof": true, "logo": true,
		"catalog": true, "catalogue": true, "sticker": true, "wallpaper": true,
		"preview": true,
	}
	coverBadPrefix = regexp.MustCompile(`^(?:cd|disc|disk|dvd|scan|booklet|inlay|tray|back|rear|inside|matrix|label|obi|spine|side)[0-9]*$`)
	wmpLargeRe     = regexp.MustCompile(`^albumart[_-].*(?:large|big)`)
	wmpSmallRe     = regexp.MustCompile(`^albumart[_-].*small`)
	wmpAlbumRe     = regexp.MustCompile(`^album[-_][0-9a-f]{16,}`)
	numberedRe     = regexp.MustCompile(`^[0-9]{1,3}$`)
	discFolderName = regexp.MustCompile(`(?i)\b(?:cd|disc|disk)[ _.-]*[0-9]+\b|^(?:cd|disc|disk|flac|wav|mp3|alac|aiff|ape|dsf|hires|audio)$`)
)

func coverTokens(stem string) []string {
	return strings.FieldsFunc(stem, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

func coverHasWord(toks []string, word string) bool {
	return slices.Contains(toks, word)
}

func coverHasPrefix(toks []string, prefix string) bool {
	for _, t := range toks {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func coverHasFrontToken(toks []string) bool {
	for _, t := range toks {
		switch {
		case t == "front", t == "cover", t == "covers", t == "folder",
			t == "frontcover", t == "coverart", t == "folderart":
			return true
		case strings.HasPrefix(t, "front"), strings.HasPrefix(t, "cover"),
			strings.HasPrefix(t, "folder"), strings.HasPrefix(t, "albumart"):
			return true
		}
	}
	return false
}

func coverHasBadToken(toks []string) bool {
	for _, t := range toks {
		if coverBadWords[t] || coverBadPrefix.MatchString(t) {
			return true
		}
	}
	return false
}

func coverWMPSmall(stem string) bool {
	return wmpSmallRe.MatchString(stem) || stem == "albumartsmall"
}

func coverWMPLarge(stem string) bool {
	return wmpLargeRe.MatchString(stem) || stem == "albumart"
}

func coverWMPAlbumCache(stem string) bool {
	return wmpAlbumRe.MatchString(stem)
}

func numberedCoverName(stem string) bool {
	return numberedRe.MatchString(stem)
}

// FindCoverCandidates walks a collection directory and returns every image
// candidate ranked best-first. Parent artwork is included only when
// IncludeParent is set and the parent is plausibly this album's parent.
func FindCoverCandidates(tc *TieClient, uid DirUID, opts CoverSearchOptions) ([]CoverCandidate, error) {
	if uid == "" {
		return nil, nil
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	var out []CoverCandidate
	seen := map[DirUID]bool{}
	var walk func(dir DirUID, rel string, depth int) error
	walk = func(dir DirUID, rel string, depth int) error {
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		d, err := ReadTieDir(tc, dir)
		if err != nil {
			if depth == 0 {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			return nil // unreadable artwork subfolder does not fail the album
		}
		for _, f := range d.Files {
			if !isCoverImageFile(f) {
				continue
			}
			addCoverCandidate(&out, f.Uid, f.Filename, pathpkg.Join(rel, f.Filename), depth)
		}
		if depth >= maxDepth {
			return nil
		}
		for _, sub := range d.SubDirs {
			name := subDirName(sub)
			if name == "" || strings.HasSuffix(strings.ToLower(name), "_prev") {
				continue
			}
			if isCollectionDir(sub) {
				continue
			}
			subRel := pathpkg.Join(rel, name)
			if err := walk(sub.Uid, subRel, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(uid, "", 0); err != nil {
		return nil, err
	}
	if opts.IncludeParent {
		out = append(out, parentCoverCandidates(tc, uid)...)
	}
	sortCoverCandidates(out)
	return out, nil
}

func addCoverCandidate(out *[]CoverCandidate, hash, name, rel string, depth int) {
	if hash == "" || name == "" {
		return
	}
	score := coverUserScore(name, rel)
	*out = append(*out, CoverCandidate{
		Hash: hash, Filename: name, Path: rel, Depth: depth,
		Score: score, Confidence: coverConfidence(CoverNameScore(name)),
	})
}

func sortCoverCandidates(c []CoverCandidate) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Score != c[j].Score {
			return c[i].Score > c[j].Score
		}
		if c[i].Depth != c[j].Depth {
			return c[i].Depth < c[j].Depth
		}
		return c[i].Path < c[j].Path
	})
}

func subDirName(sub SubDirectory) string {
	if len(sub.Paths) > 0 {
		return pathpkg.Base(sub.Paths[0])
	}
	return string(sub.Uid)
}

func isCollectionDir(sub SubDirectory) bool {
	for _, t := range sub.DirTypes {
		if IsCollectionLabel(t.String()) {
			return true
		}
	}
	return false
}

func isCoverImageFile(f File) bool {
	return f.TieType == TieImageFile || f.TieType == TieImageArchive ||
		strings.HasPrefix(f.MediaType, "image/")
}

// parentCoverCandidates looks one level up for artwork. It is deliberately
// conservative: the parent must not be a collection, must not contain audio
// files directly, and must either look like a disc/media subfolder or have
// exactly one audio-bearing child. This keeps a library root's own cover.jpg
// from becoming every album's artwork.
func parentCoverCandidates(tc *TieClient, uid DirUID) []CoverCandidate {
	row, err := tc.Get(string(uid))
	if err != nil {
		return nil
	}
	if len(collectionLabels(row)) > 0 {
		return nil
	}
	parents := RowValues(row, RelParent)
	if len(parents) == 0 {
		return nil
	}
	albumName := albumBasename(row)
	var out []CoverCandidate
	for _, parent := range parents[:1] {
		if parent == "" {
			continue
		}
		prow, err := tc.Get(parent)
		if err != nil || len(collectionLabels(prow)) > 0 {
			continue
		}
		dir, err := ReadTieDir(tc, DirUID(parent))
		if err != nil {
			continue
		}
		if anyCoverAudio(dir.Files) {
			continue
		}
		if !parentLooksLikeAlbum(tc, albumName, dir) {
			continue
		}
		for _, f := range dir.Files {
			if !isCoverImageFile(f) {
				continue
			}
			addCoverCandidate(&out, f.Uid, f.Filename, "../"+f.Filename, 1)
		}
		for _, sub := range dir.SubDirs {
			name := subDirName(sub)
			if !isArtDirName(name) {
				continue
			}
			subDir, err := ReadTieDir(tc, sub.Uid)
			if err != nil {
				continue
			}
			for _, f := range subDir.Files {
				if !isCoverImageFile(f) {
					continue
				}
				addCoverCandidate(&out, f.Uid, f.Filename, "../"+name+"/"+f.Filename, 1)
			}
		}
	}
	return out
}

func parentLooksLikeAlbum(tc *TieClient, albumName string, parent Directory) bool {
	name := strings.ToLower(strings.TrimSpace(albumName))
	if discFolderName.MatchString(name) {
		return true
	}
	// A library/artist folder with many children is not this album's parent.
	if len(parent.SubDirs) > 8 {
		return false
	}
	audioChildren := 0
	for _, sub := range parent.SubDirs {
		d, err := ReadTieDir(tc, sub.Uid)
		if err != nil {
			continue
		}
		if anyCoverAudio(d.Files) {
			audioChildren++
		}
	}
	return audioChildren == 1
}

func anyCoverAudio(files []File) bool {
	for _, f := range files {
		if f.TieType == TieAudioFile || strings.HasPrefix(f.MediaType, "audio/") {
			return true
		}
	}
	return false
}

func albumBasename(row Row) string {
	if name := RowFirst(row, RelName); name != "" {
		return name
	}
	if p := RowFirst(row, RelPath); p != "" {
		return pathpkg.Base(p)
	}
	return RowFirst(row, RelFilename)
}

// bestMemberCover ranks the image rows already gathered by IndexCollection's
// walk, so indexing a collection and resolving its cover share one rule set.
func bestMemberCover(members []memberInfo) *CoverCandidate {
	var out []CoverCandidate
	for _, m := range members {
		if !isImageRow(m.row) {
			continue
		}
		name := RowFirst(m.row, RelFilename)
		if name == "" {
			name = pathpkg.Base(m.rel)
		}
		addCoverCandidate(&out, m.row.Key, name, m.rel, strings.Count(m.rel, "/"))
	}
	if len(out) == 0 {
		return nil
	}
	sortCoverCandidates(out)
	return &out[0]
}

func isImageRow(row Row) bool {
	for _, t := range RowValues(row, RelType) {
		if t == str(TieImageFile) {
			return true
		}
	}
	return strings.HasPrefix(RowFirst(row, RelMediaType), "image/")
}

// ResolveCover finds and records a collection's source artwork. It does not
// create a thumbnail; clients scale the chosen source into the "thumbnail"
// relation when they first display it.
func ResolveCover(tc *TieClient, uid string, opts CoverOptions) (CoverResult, error) {
	if uid == "" {
		return CoverResult{}, ErrNoCover
	}
	row, err := tc.Get(uid)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return CoverResult{}, err
	}
	host := opts.Host
	if host.URL == "" {
		if h, err := tc.ResolveHost(""); err == nil {
			host = h
		}
	}

	if isArchiveRow(row) {
		if !opts.IncludeArchives {
			return CoverResult{}, ErrNoCover
		}
		data, member, err := archiveCoverBytes(tc, host, uid)
		if err != nil {
			return CoverResult{}, err
		}
		hash, err := coverArtHash(host, data, opts.DryRun)
		if err != nil {
			return CoverResult{}, err
		}
		score := CoverNameScore(member)
		if !opts.DryRun {
			if err := SetCover(tc, uid, hash, "archive:"+member); err != nil {
				return CoverResult{}, err
			}
		}
		return CoverResult{Hash: hash, Source: "archive", Path: member,
			Score: score, Confidence: coverConfidence(score), Data: data}, nil
	}

	candidates, err := FindCoverCandidates(tc, DirUID(uid), CoverSearchOptions{
		IncludeParent: opts.IncludeParent,
		MaxDepth:      opts.MaxDepth,
	})
	if err != nil {
		return CoverResult{}, err
	}
	var best *CoverCandidate
	if len(candidates) > 0 {
		best = &candidates[0]
	}

	// Embedded artwork is the fallback after high/medium file art, but it
	// beats a low-confidence back/scan/inside image.
	if opts.IncludeEmbedded && (best == nil || best.Confidence == CoverLow) {
		hashes, err := firstAudioHashes(tc, uid, 3)
		if err == nil {
			for _, hash := range hashes {
				pic, err := ExtractEmbeddedPicture(host, hash)
				if err != nil {
					continue
				}
				if len(pic) == 0 {
					continue
				}
				coverHash, err := coverArtHash(host, pic, opts.DryRun)
				if err != nil {
					continue
				}
				if !opts.DryRun {
					if err := SetCover(tc, uid, coverHash, "embedded:"+hash); err != nil {
						return CoverResult{}, err
					}
				}
				return CoverResult{Hash: coverHash, Source: "embedded", Path: hash,
					Score: 95, Confidence: CoverHigh, Data: pic}, nil
			}
		}
	}

	if best != nil {
		if !opts.DryRun {
			source := "file:" + best.Path
			if err := SetCover(tc, uid, best.Hash, source); err != nil {
				return CoverResult{}, err
			}
		}
		return CoverResult{Hash: best.Hash, Source: "file", Path: best.Path,
			Score: best.Score, Confidence: best.Confidence}, nil
	}
	return CoverResult{}, ErrNoCover
}

// RowCoverIsLow reports whether row's cover relation is a low-confidence last
// resort (a back/disc/booklet scan) rather than a real front cover. Tools that
// look for missing artwork can treat such a cover as "still missing".
func RowCoverIsLow(row Row) bool {
	if RowFirst(row, RelCover) == "" {
		return false
	}
	source := RowFirst(row, RelCoverSource)
	if !strings.HasPrefix(source, "file:") {
		return false
	}
	return CoverNameScore(strings.TrimPrefix(source, "file:")) < 50
}

// SetCover records the chosen source artwork on a collection. It does not
// touch the thumbnail cache; callers replacing a user-visible cover should
// clear "thumbnail" so the next view regenerates it.
func SetCover(tc *TieClient, uid, hash, source string) error {
	if err := tc.Set(uid, string(RelCover), []string{hash}); err != nil {
		return err
	}
	if source != "" {
		return tc.Set(uid, string(RelCoverSource), []string{source})
	}
	return nil
}

func isArchiveRow(row Row) bool {
	for _, t := range RowValues(row, RelType) {
		if strings.HasSuffix(t, "-archive") {
			return true
		}
	}
	return false
}

// firstAudioHashes returns the first audio blobs below uid in path order: the
// album's first track, or the first track of each disc for a multi-disc tree.
func firstAudioHashes(tc *TieClient, uid string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	row, err := tc.Get(uid)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if isAudioRow(row) {
		return []string{uid}, nil
	}
	type found struct {
		path string
		hash string
	}
	var files []found
	seen := map[DirUID]bool{}
	var walk func(dir DirUID, rel string, depth int) error
	walk = func(dir DirUID, rel string, depth int) error {
		if seen[dir] || depth > 3 {
			return nil
		}
		seen[dir] = true
		d, err := ReadTieDir(tc, dir)
		if err != nil {
			if depth == 0 && !errors.Is(err, ErrNotFound) {
				return err
			}
			return nil
		}
		for _, f := range d.Files {
			if f.TieType == TieAudioFile || strings.HasPrefix(f.MediaType, "audio/") {
				files = append(files, found{path: pathpkg.Join(rel, f.Filename), hash: f.Uid})
			}
		}
		for _, sub := range d.SubDirs {
			if isCollectionDir(sub) {
				continue
			}
			name := subDirName(sub)
			if name == "" || strings.HasSuffix(strings.ToLower(name), "_prev") {
				continue
			}
			if err := walk(sub.Uid, pathpkg.Join(rel, name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(DirUID(uid), "", 0); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	out := make([]string, 0, limit)
	for _, f := range files {
		if f.hash == "" {
			continue
		}
		out = append(out, f.hash)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// coverArtHash returns the content address of data, uploading it unless dryRun.
func coverArtHash(host FileHost, data []byte, dryRun bool) (string, error) {
	if len(data) == 0 {
		return "", ErrNoCover
	}
	if len(data) > maxCoverArtBytes {
		return "", fmt.Errorf("cover artwork larger than %d bytes", maxCoverArtBytes)
	}
	if host.URL == "" {
		return "", errors.New("no filehost configured for cover artwork")
	}
	pc := putlib.PutConfig{Client: HTTPClientFor(host), Store: host.Store}
	hash, err := pc.AddressOf(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	if dryRun {
		return hash, nil
	}
	item := pc.UploadMultipart(FileHostClient(host), hash, bytes.NewReader(data), len(data), "cover")
	if item.ErrorMsg != "" {
		return "", errors.New(item.ErrorMsg)
	}
	if item.Hash != hash {
		return "", errors.New("cover artwork checksum mismatch")
	}
	return hash, nil
}

const maxCoverArtBytes = 32 << 20

// --- embedded pictures ---

const (
	coverHeadBytes  = 64 << 10
	coverProbeBytes = 4 << 20
	coverTagCap     = 16 << 20
)

// ExtractEmbeddedPicture returns the first embedded picture from an audio
// blob, or (nil, nil) when the file carries none. Only the leading tag block
// is fetched where the container declares its extent (ID3v2, FLAC); other
// containers get a wider head probe and, when needed, a full read.
func ExtractEmbeddedPicture(host FileHost, hash string) ([]byte, error) {
	if hash == "" {
		return nil, ErrNoCover
	}
	if host.URL == "" {
		return nil, errors.New("no filehost configured for embedded artwork")
	}
	head, err := fetchBlobPrefix(host, hash, coverHeadBytes)
	if err == nil {
		n, exact := tagBlockSize(head)
		if exact && n <= int64(len(head)) {
			if pic, parsed := embeddedCoverIn(head[:n]); parsed {
				return pic, nil
			}
		} else if exact {
			if data, err := fetchBlobPrefix(host, hash, n); err == nil && int64(len(data)) >= n {
				if pic, parsed := embeddedCoverIn(data[:n]); parsed {
					return pic, nil
				}
			}
		} else if wide, err := fetchBlobPrefix(host, hash, coverProbeBytes); err == nil {
			pic, parsed := embeddedCoverIn(wide)
			if parsed && (pic != nil || !isMP4Head(wide) || len(wide) < coverProbeBytes) {
				return pic, nil
			}
		}
	}
	r, err := getlib.ReadFile(HTTPClientFor(host), host.URL, hash)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	meta, err := tag.ReadFrom(bytes.NewReader(data))
	if err != nil {
		return nil, nil
	}
	if pic := meta.Picture(); pic != nil {
		return pic.Data, nil
	}
	return nil, nil
}

func fetchBlobPrefix(host FileHost, hash string, n int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(host.URL, "/")+"/"+hash, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := HTTPClientFor(host).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, n))
	default:
		return nil, fmt.Errorf("filehost get %s: unexpected status %s", hash, resp.Status)
	}
}

func tagBlockSize(head []byte) (int64, bool) {
	if len(head) >= 10 && string(head[:3]) == "ID3" {
		size := int64(head[6]&0x7f)<<21 | int64(head[7]&0x7f)<<14 |
			int64(head[8]&0x7f)<<7 | int64(head[9]&0x7f)
		if size <= 0 || size > coverTagCap {
			return 0, false
		}
		total := 10 + size
		if head[5]&0x10 != 0 {
			total += 10
		}
		return total, true
	}
	if len(head) < 8 || string(head[:4]) != "fLaC" {
		return 0, false
	}
	pos := int64(4)
	for {
		if pos+4 > int64(len(head)) {
			return 0, false
		}
		h := head[pos]
		length := int64(head[pos+1])<<16 | int64(head[pos+2])<<8 | int64(head[pos+3])
		end := pos + 4 + length
		if end > coverTagCap {
			return 0, false
		}
		if h&0x80 != 0 {
			return end, true
		}
		pos = end
	}
}

func isMP4Head(prefix []byte) bool {
	return len(prefix) >= 8 && string(prefix[4:8]) == "ftyp"
}

func embeddedCoverIn(prefix []byte) (pic []byte, parsed bool) {
	if len(prefix) < 8 {
		return nil, false
	}
	switch {
	case isMP4Head(prefix), string(prefix[:4]) == "fLaC",
		string(prefix[:4]) == "OggS", string(prefix[:4]) == "DSD ",
		string(prefix[:3]) == "ID3":
	default:
		return nil, true
	}
	meta, err := tag.ReadFrom(bytes.NewReader(prefix))
	if err != nil {
		return nil, false
	}
	if pic := meta.Picture(); pic != nil {
		return pic.Data, true
	}
	return nil, true
}

// setArchiveCoverFromFile records artwork extracted from a just-imported local
// archive. It is best effort: a missing member or failed upload leaves the
// album import successful and the archive coverless.
func (tc *TieClient) setArchiveCoverFromFile(archivePath, member string, host FileHost) error {
	if member == "" {
		return nil
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	hash, err := metadata.HashReader(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rc, err := archivelib.Open(f, member)
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxCoverArtBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCoverArtBytes {
		return fmt.Errorf("archive cover %s larger than %d bytes", member, maxCoverArtBytes)
	}
	coverHash, err := coverArtHash(host, data, false)
	if err != nil {
		return err
	}
	return SetCover(tc, hash, coverHash, "archive:"+member)
}

// --- archive artwork ---

// archiveCoverBytes extracts the best image member of an archive. Zip members
// are fetched with HTTP ranges, so a multi-gigabyte archive does not need to
// be downloaded just for its cover.
func archiveCoverBytes(tc *TieClient, host FileHost, hash string) ([]byte, string, error) {
	if host.URL == "" {
		return nil, "", errors.New("no filehost configured for archive artwork")
	}
	client := HTTPClientFor(host)
	size, err := getlib.TotalSize(client, host.URL, hash)
	if err != nil {
		return nil, "", err
	}
	ra := &httpRangeReader{client: client, url: strings.TrimRight(host.URL, "/") + "/" + hash, size: size}
	head := make([]byte, 4)
	if _, err := ra.ReadAt(head, 0); err == nil && isZipHead(head) {
		data, member, err := zipCoverBytes(ra, size)
		if err != nil {
			return nil, "", err
		}
		return data, member, nil
	}
	// Non-zip archives: fall back to a full read and archivelib.
	r, err := getlib.ReadFile(client, host.URL, hash)
	if err != nil {
		return nil, "", err
	}
	blob, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	members, err := archivelib.List(bytes.NewReader(blob))
	if err != nil {
		return nil, "", err
	}
	member := bestArchiveCoverMember(members)
	if member == "" {
		return nil, "", ErrNoCover
	}
	rc, err := archivelib.Open(bytes.NewReader(blob), member)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxCoverArtBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxCoverArtBytes {
		return nil, "", fmt.Errorf("archive cover %s larger than %d bytes", member, maxCoverArtBytes)
	}
	return data, member, nil
}

func isZipHead(head []byte) bool {
	return len(head) >= 4 && head[0] == 'P' && head[1] == 'K' &&
		(head[2] == 3 || head[2] == 5)
}

func zipCoverBytes(ra io.ReaderAt, size int64) ([]byte, string, error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, "", err
	}
	var best *zip.File
	bestScore := -1
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isImageName(f.Name) {
			continue
		}
		if score := CoverNameScore(f.Name); score > bestScore {
			best, bestScore = f, score
		}
	}
	if best == nil {
		return nil, "", ErrNoCover
	}
	rc, err := best.Open()
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxCoverArtBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxCoverArtBytes {
		return nil, "", fmt.Errorf("archive cover %s larger than %d bytes", best.Name, maxCoverArtBytes)
	}
	return data, best.Name, nil
}

func isImageName(name string) bool {
	switch strings.ToLower(pathpkg.Ext(name)) {
	case ".jpg", ".jpeg", ".jfif", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

// bestArchiveCoverMember returns the member name of the best cover image.
func bestArchiveCoverMember(members []archivelib.Member) string {
	best, bestScore := "", -1
	for _, m := range members {
		if m.Kind != archivelib.Image || !isImageName(m.Name) {
			continue
		}
		if score := CoverNameScore(m.Name); score > bestScore {
			best, bestScore = m.Name, score
		}
	}
	return best
}

// httpRangeReader is the minimal io.ReaderAt a zip central directory needs.
type httpRangeReader struct {
	client *http.Client
	url    string
	size   int64
}

func (r *httpRangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	end := off + int64(len(p)) - 1
	if end >= r.size {
		end = r.size - 1
	}
	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return readAtFull(resp.Body, p)
	case http.StatusOK:
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return 0, err
		}
		return readAtFull(resp.Body, p)
	default:
		return 0, fmt.Errorf("filehost range get: unexpected status %s", resp.Status)
	}
}

func readAtFull(r io.Reader, p []byte) (int, error) {
	n, err := io.ReadFull(r, p)
	if errors.Is(err, io.ErrUnexpectedEOF) && n > 0 {
		return n, io.EOF
	}
	return n, err
}
