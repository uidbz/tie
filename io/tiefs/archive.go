package tiesfs

import (
	"path"
	"sort"
	"strconv"

	"github.com/uidbz/tie/io/archivelib"
)

// ArchiveMemberView pairs an archive member with its flattened display name.
type ArchiveMemberView struct {
	display string
	member  archivelib.Member
}

// Display returns the name the member is listed under.
func (v ArchiveMemberView) Display() string { return v.display }

// Member returns the underlying archive member.
func (v ArchiveMemberView) Member() archivelib.Member { return v.member }

// ArchiveMemberViews maps members to disambiguated base-name display entries.
// Nested member paths are flattened to their base name (an image viewer wants
// the pictures, not the internal folder layout); base-name collisions are
// disambiguated deterministically. It sorts by full member name first so
// repeated listings and lookups agree on every call.
func ArchiveMemberViews(members []archivelib.Member) []ArchiveMemberView {
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	views := make([]ArchiveMemberView, 0, len(members))
	seen := make(map[string]int)
	for _, m := range members {
		base := path.Base(m.Name)
		if base == "" || base == "." || base == "/" {
			continue
		}
		n := seen[base]
		seen[base]++
		display := base
		if n > 0 {
			display = SuffixName(base, strconv.Itoa(n))
		}
		views = append(views, ArchiveMemberView{display: display, member: m})
	}
	return views
}
