package api

import (
	"errors"

	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

// resolveReadCollection resolves the target of a read request. An absent
// collection reports found=false: the handler then answers exactly as it
// would for an empty collection, so clients keep mapping the reply to
// ErrNotFound. Reads never create collections (a read-role caller must not
// be able to mint one per name); a malformed namespace/collection id is a
// hard error.
func resolveReadCollection(env *ws.Environment, namespace, collection string) (*tiedb.Collection, bool, error) {
	col, err := env.Collection(namespace, collection)
	if errors.Is(err, ws.ErrCollectionNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return col, true, nil
}
