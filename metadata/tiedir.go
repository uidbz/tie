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
	"github.com/uidbz/tie/tiedb"
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

// HexHashBlobPolicy returns the tiedb blob policy that stores content-address
// hashes as raw 32-byte entries instead of chunking their 64-char hex form into
// the trie. A hash is the worst case for the chunk trie (no shared prefixes, 3
// entries per hash); this collapses it to one. Encode matches only well-formed
// hex hashes; Decode re-hex-encodes the raw bytes back to the stored string.
func HexHashBlobPolicy() *tiedb.BlobPolicy {
	return &tiedb.BlobPolicy{
		Encode: func(value string) ([]byte, bool) {
			if !IsHexHash(value) {
				return nil, false
			}
			raw, err := hex.DecodeString(value)
			if err != nil {
				return nil, false
			}
			return raw, true
		},
		Decode: func(raw []byte) string {
			return hex.EncodeToString(raw)
		},
	}
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
// earlier columns. The filename is percent-encoded for the same reason: Linux
// allows TAB and LF in a name, and a raw one either shifts the columns or
// splits the line (§4.8).
func (e DirEntry) Line() string {
	return e.Hash + "\t" + encodeName(e.Filename) + "\t" + strconv.Itoa(e.Size) + "\t" + EncodeHead(e.Head) + "\n"
}

// encodeName escapes the characters that would break a tiedir line's column or
// line structure: '%', TAB, LF and CR. Percent-encoding was chosen over
// rejecting such names because they are legal on Linux and losing an entry
// silently is worse than escaping it — a manifest line that fails to parse is
// skipped by every reader, including the reaper's dirChildren, which stops
// protecting a child that is still referenced.
func encodeName(name string) string {
	if !strings.ContainsAny(name, "%\t\n\r") {
		return name
	}
	var b strings.Builder
	b.Grow(len(name) + 8)
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '%':
			b.WriteString("%25")
		case '\t':
			b.WriteString("%09")
		case '\n':
			b.WriteString("%0A")
		case '\r':
			b.WriteString("%0D")
		default:
			b.WriteByte(name[i])
		}
	}
	return b.String()
}

// decodeName expands %XX escapes. It is deliberately lenient: only a '%'
// followed by two hex digits is expanded, so a manifest written before names
// were escaped — where a name could contain a bare '%' — still reads back
// unchanged. The one ambiguity is an old manifest whose name literally spells
// an escape (say "a%09b"); it reads back decoded. Only the displayed name can
// differ; hashes and sizes are unaffected.
func decodeName(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHexDigit(s[i+1]) && isHexDigit(s[i+2]) {
			b.WriteByte(hexVal(s[i+1])<<4 | hexVal(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
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
	// Validate the decoded name: escaping means a manifest can spell a
	// separator or a traversal name as %XX, and the check has to run on what
	// the reader would actually use.
	name := decodeName(parts[1])
	if !isValidEntryName(name) {
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
	return DirEntry{Hash: parts[0], Filename: name, Size: size, Head: head}, true
}

// ValidEntryName reports whether name can be stored in a tie directory
// manifest: a single, safe path component. Writers should check it before
// uploading a child, because an entry that cannot be represented is dropped by
// every reader — and an unreferenced child is no longer protected by the
// reaper.
func ValidEntryName(name string) bool { return isValidEntryName(name) }

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
