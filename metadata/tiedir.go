package metadata

import (
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
	"strconv"
	"strings"

	"github.com/h2non/filetype"
	"github.com/minio/highwayhash"
)

// MagicNumber is how many leading bytes of a file are needed to sniff its
// media type.
const MagicNumber = 261

// DirHeader prefixes every tie directory blob. A directory's hash changes
// whenever its contents change, which gives git-like versioning for free.
const DirHeader = "tiedir-v2\n---\n"

// tieKey is the fixed highwayhash key that defines tie's content address space.
// Upload and download must agree on it, so it lives here rather than in putlib.
const tieKey = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"

// hashKey is tieKey decoded once. It is nil only if the constant is malformed,
// which is a build-time programming error rather than a runtime condition.
var hashKey []byte

func init() {
	k, err := hex.DecodeString(tieKey)
	if err != nil {
		panic("metadata: invalid tieKey constant: " + err.Error())
	}
	hashKey = k
}

// NewHash returns a fresh keyed highwayhash. All content addresses in tie are
// computed with it, so upload hashing and download verification stay identical.
func NewHash() (hash.Hash, error) {
	return highwayhash.New(hashKey)
}

// HashReader returns the tie content address of everything read from r.
func HashReader(r io.Reader) (string, error) {
	h, err := NewHash()
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// IsHexHash reports whether s is a well-formed content address: exactly 64
// lowercase hex characters. Enforced on parse so an untrusted manifest cannot
// smuggle a traversal or SSRF payload through the hash column.
func IsHexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

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
// The filename must be a single path component: entries store a child's own
// name, never a path, so a crafted manifest cannot escape the checkout root via
// "..", an absolute path, or a nested separator.
func ParseDirLine(line string) (DirEntry, bool) {
	parts := strings.Split(line, "\t")
	if len(parts) != 4 {
		return DirEntry{}, false
	}
	if !IsHexHash(parts[0]) {
		return DirEntry{}, false
	}
	if !isValidEntryName(parts[1]) {
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

// isValidEntryName reports whether name is a safe single path component: no
// separators, not empty, and not the "." / ".." traversal names.
func isValidEntryName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
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
