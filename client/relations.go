package client

import (
	"errors"

	"github.com/uidbz/tie/metadata"
)

// Edges between content hashes — relating one file to another and reading the
// relations back. Split out of tag.go (§7.11).

// RelateFiles records an open-vocabulary relation between two media items by
// content hash, e.g. RelateFiles(track, "sampled-from", source). Both directed
// edges are written as forward triples — (from, relation, to) and
// (to, relation, from) — because forward associations are always stored (the
// reverse index is a fixed allowlist that arbitrary relation names are not in).
// Writing both directions makes the relation browsable from either media item
// with a plain forward lookup. relation must not be one of the reserved metadata
// relations (filename, tag, parent, ...).
func (tie *TieClient) RelateFiles(fromHash, relation, toHash string) error {
	if !metadata.IsHexHash(fromHash) || !metadata.IsHexHash(toHash) {
		return errors.New("RelateFiles: both endpoints must be content hashes")
	}
	if relation == "" {
		return errors.New("RelateFiles: relation must not be empty")
	}
	b := tie.NewBatch()
	b.Add(fromHash, relation, toHash)
	b.Add(toHash, relation, fromHash)
	if _, err := tie.Batch(b); err != nil {
		return err
	}
	return nil
}

// RelationsFrom returns every media-to-media relation recorded on hash. Because
// RelateFiles writes both directions as forward edges, this one forward lookup
// surfaces relations pointing both from and into hash. Only triples whose object
// is itself a content hash are returned, so file metadata (filename, tag, ...) is
// excluded.
func (tie *TieClient) RelationsFrom(hash string) ([]MediaRelation, error) {
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rels []MediaRelation
	for relation, values := range row.Attributes {
		for _, value2 := range values {
			if metadata.IsHexHash(value2) {
				rels = append(rels, MediaRelation{FromHash: hash, Relation: relation, ToHash: value2})
			}
		}
	}
	return rels, nil
}
