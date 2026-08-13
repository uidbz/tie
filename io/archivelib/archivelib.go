// Package archivelib expands archive blobs into their member listing and opens
// individual members, from any seekable stream. It is client-side only — the
// filehost stays a pure content-addressed byte store and never parses archives.
// archivelib deliberately does not import the client package (which maps Kind to
// a tie-type), so it can be reused by the FUSE mounts and the CLI without an
// import cycle.
//
// Archive access is backed by github.com/mholt/archives, the same library the
// imgview/tieview viewer uses, so tie recognizes the same formats (zip, tar,
// rar, 7z, cbr, ...) and behaves identically to the viewer's local-archive path.
package archivelib

import (
	"context"
	"io"
	"io/fs"

	"github.com/h2non/filetype"
	"github.com/mholt/archives"
)

// Stream is the input an archive is read from: a seekable, random-access byte
// source (os.File, bytes.Reader, io.SectionReader all satisfy it). mholt's
// FileSystem needs io.ReaderAt+io.Seeker for safe concurrent access and
// io.Reader for format sniffing.
type Stream = archives.ReaderAtSeeker

// headSize is how many leading bytes of a member we sniff for classification —
// the same window client.GetTieType uses for standalone files, so a member
// classifies identically to the same bytes on disk.
const headSize = 261

// Kind is the neutral media category of a member, mirroring the file tie-types
// without depending on the client package.
type Kind int

const (
	Unknown Kind = iota
	Image
	Audio
	Video
	Document
)

// Member is one file entry inside an archive.
type Member struct {
	Name string
	Size int64
	Head []byte
	Kind Kind
}

func classify(head []byte) Kind {
	switch {
	case filetype.IsImage(head):
		return Image
	case filetype.IsVideo(head):
		return Video
	case filetype.IsAudio(head):
		return Audio
	case filetype.IsDocument(head):
		return Document
	default:
		return Unknown
	}
}

// fsFor builds a read-only fs.FS over an archive stream. The format is detected
// from the stream's content (no filename hint needed for content-addressed
// blobs). mholt identifies the format by reading from the stream's current
// offset, so we rewind first — callers often hand us a file positioned past the
// header (a cache fd left at EOF after download, or a fd advanced by a type
// sniff). Give each concurrent reader its own zero-based view (io.SectionReader)
// to keep this rewind race-free.
func fsFor(r Stream) (fs.FS, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return archives.FileSystem(context.Background(), "", r)
}

// List enumerates the non-directory members of an archive, sniffing each
// member's leading bytes to classify it. Members that fail to open are skipped.
func List(r Stream) ([]Member, error) {
	fsys, err := fsFor(r)
	if err != nil {
		return nil, err
	}
	var members []Member
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		head := make([]byte, headSize)
		n, _ := io.ReadFull(f, head)
		f.Close()
		head = head[:n]
		var size int64
		if info, err := d.Info(); err == nil {
			size = info.Size()
		}
		members = append(members, Member{
			Name: p,
			Size: size,
			Head: head,
			Kind: classify(head),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return members, nil
}

// Open returns a reader over a single member's decompressed bytes. The returned
// stream is forward-only; callers needing random access must copy it to a
// seekable file first.
func Open(r Stream, name string) (io.ReadCloser, error) {
	fsys, err := fsFor(r)
	if err != nil {
		return nil, err
	}
	return fsys.Open(name)
}

// ModalKind returns the most common recognized member kind, ignoring Unknown
// members. An album of tracks with one cover image resolves to Audio; a scan
// dump of images resolves to Image. Returns Unknown when nothing is recognized.
func ModalKind(members []Member) Kind {
	counts := make(map[Kind]int)
	for _, m := range members {
		if m.Kind != Unknown {
			counts[m.Kind]++
		}
	}
	best, bestN := Unknown, 0
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}
