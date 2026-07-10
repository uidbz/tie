package metadata

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/h2non/filetype"
)

// MagicNumber is how many leading bytes of a file are needed to sniff its
// media type.
const MagicNumber = 261

// DirHeader prefixes every tie directory blob. A directory's hash changes
// whenever its contents change, which gives git-like versioning for free.
const DirHeader = "tiedir-v2\n---\n"

// DirEntry is one line of a tiedir blob. The head column stores the file's
// first bytes (rather than a derived media type) so classification stays
// recomputable as detectors improve.
type DirEntry struct {
	Hash     string
	Filename string
	Size     int
	Head     []byte
}

// Line renders the entry as a tiedir-v2 line (trailing newline included). The
// head bytes are base64-encoded so tabs/newlines in the raw bytes cannot break
// column or line splitting; head is last so a malformed value cannot shift
// earlier columns.
func (e DirEntry) Line() string {
	return e.Hash + "\t" + e.Filename + "\t" + strconv.Itoa(e.Size) + "\t" + EncodeHead(e.Head) + "\n"
}

// ParseDirLine parses one tiedir-v2 line. It returns false for malformed lines.
func ParseDirLine(line string) (DirEntry, bool) {
	parts := strings.Split(line, "\t")
	if len(parts) != 4 {
		return DirEntry{}, false
	}
	size, err := strconv.Atoi(parts[2])
	if err != nil {
		return DirEntry{}, false
	}
	head, err := DecodeHead(parts[3])
	if err != nil {
		return DirEntry{}, false
	}
	return DirEntry{Hash: parts[0], Filename: parts[1], Size: size, Head: head}, true
}

func EncodeHead(head []byte) string {
	return base64.StdEncoding.EncodeToString(head)
}

func DecodeHead(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// IsDirHead reports whether head bytes belong to a tie directory blob (any
// tiedir version), used to classify an entry as a directory.
func IsDirHead(head []byte) bool {
	return strings.HasPrefix(string(head), "tiedir-v")
}

// MediaType classifies head bytes into a MIME type. It is recomputable from the
// head stored in each tiedir entry.
func MediaType(head []byte) string {
	if IsDirHead(head) {
		return "inode/directory"
	}
	if t, err := filetype.Get(head); err == nil && t != filetype.Unknown {
		return t.MIME.Value
	}
	return "application/octet-stream"
}
