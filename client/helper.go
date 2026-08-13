package client

import (
	"io"
	"os"

	"git.sr.ht/~uid/tie/io/archivelib"
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
