package client

import (
	"errors"
	"sort"
	"strings"
)

// Favorite tags are recorded in the ("tags","favorite",<tag>) registry, a
// parallel to the ("tags","all",<tag>) tag registry that RegisterTag/ListTags
// maintain. It marks a tag as pinned/starred without attaching it to any file,
// so the favorite set is shared across every client on the collection.

// ListFavorites returns the registered favorite tags, sorted. A collection with
// no favorites yields an empty slice, not an error.
func (tie *TieClient) ListFavorites() ([]string, error) {
	row, err := tie.Get(str(TieTags))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	favorites := RowValues(row, str(TieFavorite))
	sort.Strings(favorites)
	return favorites, nil
}

// RegisterFavorite marks tag as a favorite in the ("tags","favorite",<tag>)
// registry. Registering an existing favorite is a harmless no-op. An empty tag
// is rejected.
func (tie *TieClient) RegisterFavorite(tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return errors.New("RegisterFavorite: tag must not be empty")
	}
	if _, err := tie.Add(str(TieTags), str(TieFavorite), tag); err != nil {
		return err
	}
	return tie.Sync()
}

// UnregisterFavorite removes tag from the ("tags","favorite",<tag>) registry.
// Unregistering a tag that is not a favorite is a harmless no-op. An empty tag
// is rejected.
func (tie *TieClient) UnregisterFavorite(tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return errors.New("UnregisterFavorite: tag must not be empty")
	}
	if _, err := tie.Delete(str(TieTags), str(TieFavorite), tag); err != nil {
		return err
	}
	return tie.Sync()
}
