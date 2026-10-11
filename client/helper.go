package client

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/h2non/filetype"
	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/metadata/tag"
)

// GetTieType classifies a file by sniffing its leading bytes. An empty or
// short file is a valid input — it simply classifies on whatever bytes exist
// (an empty file is unknown-file) — so io.EOF / io.ErrUnexpectedEOF from the
// read are not errors. Only a genuine read failure is returned. This mirrors
// archivelib's member sniffing, which uses the same 261-byte window.
func GetTieType(file io.Reader) (TieType, error) {
	head := make([]byte, 261)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return TieUnknownFile, err
	}
	head = head[:n]

	switch true {
	case filetype.IsImage(head):
		return TieImageFile, nil
	case filetype.IsVideo(head):
		return TieVideoFile, nil
	case filetype.IsAudio(head):
		return TieAudioFile, nil
	case filetype.IsDocument(head):
		return TieDocumentFile, nil
	case filetype.IsArchive(head):
		return TieArchiveFile, nil
	default:
		return TieUnknownFile, nil
	}
}

func GetTieTypeFromPath(path string) (TieType, error) {
	file, err := os.Open(path)
	if err != nil {
		return TieUnknownFile, err
	}
	defer file.Close()

	t, err := GetTieType(file)
	if err != nil {
		return TieUnknownFile, err
	}
	// An archive refines to a media-specific *-archive by peeking at its
	// members: the fd is a seekable ReaderAt, so we can open it as a zip
	// without buffering. A failure to open leaves it as the generic
	// archive-file (e.g. a non-zip archive we don't expand yet).
	if t == TieArchiveFile {
		if members, err := archivelib.List(file); err == nil {
			return ArchiveTieType(archivelib.ModalKind(members)), nil
		}
	}
	return t, nil
}

// ArchiveTieType maps an archivelib member-kind to the archive tie-type stored
// on the blob. An unrecognized/mixed archive stays the generic archive-file.
func ArchiveTieType(k archivelib.Kind) TieType {
	switch k {
	case archivelib.Image:
		return TieImageArchive
	case archivelib.Audio:
		return TieAudioArchive
	case archivelib.Video:
		return TieVideoArchive
	case archivelib.Document:
		return TieDocumentArchive
	default:
		return TieArchiveFile
	}
}

// ExtractMediaMetadata reads embedded media tags from a local file. Audio files
// yield title/artist/album/year/track and playing-time duration via the vendored
// tie/tag fork; other types yield an empty Media (only placement templates and
// metadata triples consume this, and only audio currently carries usable tags).
// A read/parse failure is not fatal — it just means no metadata, so the caller
// falls back to path-based placement.
func ExtractMediaMetadata(path string) metadata.Media {
	f, err := os.Open(path)
	if err != nil {
		return metadata.Media{}
	}
	defer f.Close()
	return ExtractMediaMetadataFrom(f)
}

// ExtractMediaMetadataFrom is ExtractMediaMetadata over an already open
// stream (e.g. a downloaded blob, for `tie media backfill --rescan`). Non-audio
// content yields an empty Media.
func ExtractMediaMetadataFrom(f io.ReadSeeker) metadata.Media {
	tieType, err := GetTieType(f)
	if err != nil || tieType != TieAudioFile {
		return metadata.Media{}
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return metadata.Media{}
	}
	m, err := tag.ReadFrom(f)
	if err != nil {
		return metadata.Media{}
	}
	track, trackTotal := m.Track()
	disc, discTotal := m.Disc()
	return metadata.Media{
		Title:       m.Title(),
		Artist:      m.Artist(),
		AlbumArtist: m.AlbumArtist(),
		Album:       m.Album(),
		Year:        m.Year(),
		Track:       track,
		Disc:        disc,
		DiscTotal:   discTotal,
		TrackTotal:  trackTotal,
		Genre:       strings.TrimSpace(m.Genre()),
		Composer:    strings.TrimSpace(m.Composer()),
		Duration:    m.Duration().Seconds(),
	}
}

// SplitGenres splits a raw GENRE tag into its individual genres. Multi-value
// genre tags are stored inconsistently across taggers (";", "/", "," or NUL
// separators; ID3v1 numeric "(17)" references are already resolved by the tag
// reader), so all of them separate. Values are trimmed, empty ones dropped and
// duplicates (case-insensitive) collapsed, keeping first-seen spelling.
func SplitGenres(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == '/' || r == ',' || r == 0
	})
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		k := strings.ToLower(p)
		if p == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}
