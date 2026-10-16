package client

import (
	"io"
	"os"
	"strings"

	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/metadata/tag"
)

// Single-open ingest (§5.2 item 3).
//
// An imported file used to be opened four or five times: the uploader hashed
// it, opened it again to sniff its media type, and a third time to stream it;
// GetTieTypeFromPath opened it to classify, and ExtractMediaMetadata opened it
// yet again and re-ran the same sniff before parsing the tags. On the network
// mounts the CLAUDE.md notes call out, the open and the first-block read are
// the dominant cost, so the repeats are not free.
//
// ingest answers everything an import needs from the *content* — tie-type and
// embedded tags — from one open. The uploader still opens the file to hash and
// stream it (that is its job, and #51's HEAD-skip already avoids the streaming
// open when the blob is stored), so a re-import of unchanged content costs two
// opens where it used to cost five.

// classifyAt types an open file from its magic bytes. A generic archive is
// refined to a media-specific *-archive by peeking at its members: the handle
// is a seekable ReaderAt, so the archive is read without buffering. A failure
// to open the archive leaves the generic archive-file type.
func classifyAt(f *os.File) (TieType, error) {
	t, err := GetTieType(f)
	if err != nil {
		return TieUnknownFile, err
	}
	if t == TieArchiveFile {
		if members, err := archivelib.List(f); err == nil {
			return ArchiveTieType(archivelib.ModalKind(members)), nil
		}
	}
	return t, nil
}

// mediaTags reads the embedded media tags of an already classified file from
// the same handle. Only audio carries usable tags today; anything else yields
// an empty Media. A parse failure is not fatal — it means no metadata, and the
// caller falls back to path-based placement.
func mediaTags(f io.ReadSeeker, tieType TieType) metadata.Media {
	if tieType != TieAudioFile {
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

// ingest opens a file once and returns its tie-type and embedded media tags.
func ingest(path string) (TieType, metadata.Media, error) {
	f, err := os.Open(path)
	if err != nil {
		return TieUnknownFile, metadata.Media{}, err
	}
	defer f.Close()

	tieType, err := classifyAt(f)
	if err != nil {
		return TieUnknownFile, metadata.Media{}, err
	}
	return tieType, mediaTags(f, tieType), nil
}

// ingestFor is ingest with the lenient failure mode a tree import needs: the
// blob is already on the filehost by the time it runs, so refusing the file
// would strand it as an untagged upload. Warn and tag it unknown instead.
func ingestFor(tc *TieClient, path string) (TieType, metadata.Media) {
	tieType, media, err := ingest(path)
	if err != nil {
		tc.log().Warn("cannot classify file, tagging it unknown", "path", path, "error", err)
		return TieUnknownFile, media
	}
	return tieType, media
}
