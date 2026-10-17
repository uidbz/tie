package client

import (
	"errors"
	"sort"
	"strings"
)

// Dir-types: the labels a directory carries (audio-dir, image-dir, …) and the
// registry of known labels. Split out of tag.go (§7.11).

// typeRegistrySubject is the subject of the dir-type registry: a directory's
// classification labels are registered as (types,"all",<label>) so /query/types
// can list every custom label in use, mirroring the (tags,"all",<tag>) registry
// that Tag/SetTags write. The structural "directory" marker is never registered.
const typeRegistrySubject = "types"

func (tie *TieClient) SetDirType(uid DirUID, dirType string) error {
	if _, err := tie.Add(str(uid), string(RelType), dirType); err != nil {
		return err
	}
	if dirType != str(TieDirectory) {
		if _, err := tie.Add(typeRegistrySubject, string(RelAll), dirType); err != nil {
			return err
		}
	}
	return nil
}

// GetDirType returns a directory's classification labels — its
// (uid,"tie-type",*) values with the structural "directory" marker filtered out
// — sorted. The marker is what ReadTieDir uses to tell subdirs from files, so it
// is never surfaced here. A directory with no extra labels returns an empty
// slice, not an error.
func GetDirType(tie *TieClient, uid DirUID) ([]string, error) {
	row, err := tie.Get(string(uid))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var labels []string
	for _, t := range RowValues(row, RelType) {
		if t != str(TieDirectory) {
			labels = append(labels, t)
		}
	}
	sort.Strings(labels)
	return labels, nil
}

// SetDirTypes replaces a directory's classification labels with newLabels,
// diffing against the current labels: added labels get (uid,"tie-type",<label>)
// plus the (types,"all",<label>) registry entry, removed labels get a Delete.
// The structural "directory" marker is preserved untouched (never added, never
// removed), so a text-editor rewrite of the .type file cannot break navigation.
// Empty entries are ignored. The change is committed and synced.
func SetDirTypes(tie *TieClient, uid DirUID, newLabels []string) error {
	current, err := GetDirType(tie, uid)
	if err != nil {
		return err
	}

	want := make(map[string]bool, len(newLabels))
	for _, t := range newLabels {
		if t = strings.TrimSpace(t); t != "" && t != str(TieDirectory) {
			want[t] = true
		}
	}
	have := make(map[string]bool, len(current))
	for _, t := range current {
		have[t] = true
	}

	batch := tie.NewBatch()
	changed := false
	for t := range want {
		if !have[t] {
			batch.Add(string(uid), string(RelType), t)
			batch.Add(typeRegistrySubject, string(RelAll), t)
			changed = true
		}
	}
	for t := range have {
		if !want[t] {
			batch.Delete(string(uid), string(RelType), t)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return nil
}

// ListDirTypes returns the dir-type classification labels known to the store: the
// custom labels registered in (types,"all",<label>) unioned with the built-in
// directory TieTypes (the *-dir / *-archive / directory stringers), sorted and
// de-duplicated. The union means /query/types shows both user-created labels and
// the built-in vocabulary without pre-seeding the registry with built-ins.
func (tie *TieClient) ListDirTypes() ([]string, error) {
	set := make(map[string]bool)
	for t := TieImageDir; t <= TieDirectory; t++ {
		set[t.String()] = true
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{typeRegistrySubject}, Filter: string(RelAll)})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	for _, row := range rows {
		for _, value2 := range RowValues(row, RelAll) {
			set[value2] = true
		}
	}
	labels := make([]string, 0, len(set))
	for t := range set {
		labels = append(labels, t)
	}
	sort.Strings(labels)
	return labels, nil
}
