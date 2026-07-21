package client

import (
	"io"
	"os"

	"git.sr.ht/~uid/tie/metadata"
	"github.com/dhowden/tag"
	"github.com/h2non/filetype"
)

func GetTieType(file io.Reader) (TieType, error) {
	head := make([]byte, 261)
	if _, err := file.Read(head); err != nil {
		return TieUnknownFile, err
	}

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
	defer file.Close()
	if err != nil {
		return TieUnknownFile, err
	}
	return GetTieType(file)
}

// ExtractMediaMetadata reads embedded media tags from a local file. Audio files
// yield title/artist/album/year/track via dhowden/tag; other types yield an
// empty Media (only placement templates and metadata triples consume this, and
// only audio currently carries usable tags). A read/parse failure is not fatal —
// it just means no metadata, so the caller falls back to path-based placement.
func ExtractMediaMetadata(path string) metadata.Media {
	f, err := os.Open(path)
	if err != nil {
		return metadata.Media{}
	}
	defer f.Close()

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
	track, _ := m.Track()
	return metadata.Media{
		Title:  m.Title(),
		Artist: m.Artist(),
		Album:  m.Album(),
		Year:   m.Year(),
		Track:  track,
	}
}
