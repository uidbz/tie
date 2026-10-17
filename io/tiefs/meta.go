package tiefs

import (
	"strings"

	"github.com/uidbz/tie/client"
)

// MetaBinding binds one editable set of string values - a content hash's tags
// (TagsBinding) or a directory's tie-type labels (DirTypesBinding) - to its
// load/store functions. Transports render it as a text file: one value per
// line; writing the file replaces the set. The empty set clears it.
type MetaBinding struct {
	Load  func() ([]string, error)
	Store func([]string) error
}

// TagsBinding backs one content hash's tags.
func (t *Tree) TagsBinding(hash string) MetaBinding {
	return MetaBinding{
		Load:  func() ([]string, error) { return client.GetTags(t.Tie, hash) },
		Store: func(v []string) error { return client.SetTags(t.Tie, hash, v) },
	}
}

// DirTypesBinding backs one directory's tie-type classification labels (the
// structural "directory" marker is preserved by SetDirTypes and never
// surfaced by GetDirType, so editing this file can't break navigation).
func (t *Tree) DirTypesBinding(uid client.DirUID) MetaBinding {
	return MetaBinding{
		Load:  func() ([]string, error) { return client.GetDirType(t.Tie, uid) },
		Store: func(v []string) error { return client.SetDirTypes(t.Tie, uid, v) },
	}
}

// RenderMeta returns the values as newline-terminated bytes. Errors surface
// as empty content so a stat/read never fails; the write path reports real
// errors.
func RenderMeta(vals []string) []byte {
	if len(vals) == 0 {
		return nil
	}
	return []byte(strings.Join(vals, "\n") + "\n")
}

// ParseLines splits meta-file contents into individual values: one value per
// line, surrounding whitespace trimmed, blank lines dropped.
func ParseLines(data []byte) []string {
	var vals []string
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			vals = append(vals, t)
		}
	}
	return vals
}

// TypeInodeKey derives the inode key for a directory's .type file. It must
// differ from the .tags key (the bare DirUID, used for the directory's own tag
// file) so the two control files in one directory get distinct inodes.
func TypeInodeKey(uid client.DirUID) string { return string(uid) + "\x00type" }

// TagsSelfName is the fixed filename inside every /tags directory that exposes
// the directory's own tags for editing. It is a meta file bound to the
// directory's DirUID rather than a content hash. A real child file literally
// named ".tags" would be shadowed by it, which is why a dotfile name is used.
const TagsSelfName = ".tags"

// TagsTypeName is the fixed filename inside every /tags directory that exposes
// the directory's own tie-type classification labels for editing. Like .tags
// it uses a dotfile name so a real child file named ".type" isn't shadowed.
const TagsTypeName = ".type"
