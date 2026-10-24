package tiedb

import (
	"errors"
	"sort"
)

// DirSpec is the shape of a directory node for MkDir: which relations the store
// should write when it creates one. The triplestore is vocabulary-agnostic, so
// the caller (the tie client) names them; the store only guarantees that the
// check and the writes happen together.
type DirSpec struct {
	// Path is the virtual path the node stands for, e.g. "tie:/music/live".
	Path string
	// NewUID is the subject to create when no one owns Path yet.
	NewUID string
	// ParentUID is the node one level up, the value of ParentRelation.
	ParentUID string
	// PathRelation holds the path on the node (NewUID -> Path).
	PathRelation string
	// ParentRelation holds the edge to the parent (NewUID -> ParentUID).
	ParentRelation string
	// TypeRelation/TypeValue mark the node as a directory (NewUID -> "directory").
	TypeRelation string
	TypeValue    string
}

// MkDir creates the node for spec.Path unless some subject already owns that
// path, and reports the UID the path has either way.
//
// The lookup and the writes happen under one changeMutex hold. That is the
// point: the client-side equivalent — ask for the UID of the path, see none,
// mint one, write it — lets two concurrent creators (two imports, a FUSE mkdir
// racing an import, an SMB client) both see "no UID" and both mint one, which
// is where the duplicate-path state that verify and the dedup script exist to
// clean up comes from (§4.10).
//
// existed reports that the path was already owned; nothing was written then,
// and uid is the owner's — not the caller's NewUID.
func (ic *Collection) MkDir(spec DirSpec) (uid string, existed bool, err error) {
	if spec.Path == "" || spec.NewUID == "" || spec.PathRelation == "" {
		return "", false, errors.New("mkdir: path, new uid and path relation are required")
	}

	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	if err := ic.Err(); err != nil {
		return "", false, err
	}
	if ic.readOnly {
		return "", false, ErrReadOnly
	}

	if owner, found := ic.uidForValueLocked(spec.Path, spec.PathRelation); found {
		return owner, true, nil
	}

	if spec.ParentRelation != "" && spec.ParentUID != "" {
		if err := ic.addLocked(spec.NewUID, spec.ParentRelation, spec.ParentUID); err != nil {
			return "", false, err
		}
	}
	if err := ic.addLocked(spec.NewUID, spec.PathRelation, spec.Path); err != nil {
		return "", false, err
	}
	if spec.TypeRelation != "" && spec.TypeValue != "" {
		if err := ic.addLocked(spec.NewUID, spec.TypeRelation, spec.TypeValue); err != nil {
			return "", false, err
		}
	}
	return spec.NewUID, false, nil
}

// uidForValueLocked resolves the subject that owns value under relation,
// through the reverse association index — the same lookup a reverse query does,
// answered without a round trip. The caller must hold changeMutex (reads take no
// lock of their own).
//
// When more than one subject claims the value — the duplicate-path state a
// damaged collection can hold — the smallest is returned, so every caller gets
// the same answer instead of one at random from the map.
func (ic *Collection) uidForValueLocked(value, relation string) (string, bool) {
	keys, found := ic.GetReverse(value, relation)
	if !found || len(keys) == 0 {
		return "", false
	}
	if len(keys) == 1 {
		return keys[0], true
	}
	sort.Strings(keys)
	return keys[0], true
}
