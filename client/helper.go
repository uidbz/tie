package client

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/h2non/filetype"
	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/metadata"
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

	return classifyAt(file)
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
	if err != nil {
		return metadata.Media{}
	}
	return mediaTags(f, tieType)
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
