package client

import (
	"context"
	"time"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/tiedb"
)

// Free-text search: the client side of the Search request. Search reads the
// triplestore's in-memory index, so it costs no blob downloads and no record
// scans on the server beyond the first corpus build.
//
// SearchSpec naming mirrors QuerySpec: Relations are the searchable predicates,
// TieTypes narrows candidates by media/directory type. An empty Relations list
// returns no matches; use a SearchProfile for the common vocabularies.

// SearchMode is re-exported so callers need only the client package.
type SearchMode = tiedb.SearchMode

const (
	SearchWords     = tiedb.SearchWords
	SearchSubstring = tiedb.SearchSubstring
	SearchPrefix    = tiedb.SearchPrefix
	SearchFuzzy     = tiedb.SearchFuzzy
)

// SearchMatch is one ranked subject match: the key, the field that contributed
// most to the score, and the value in that field. Attributes is populated only
// when SearchSpec.Expand is set.
type SearchMatch struct {
	Key        string
	Relation   Relation
	Value      string
	Score      int
	Attributes map[string][]string
}

// SearchSpec describes one free-text search.
type SearchSpec struct {
	Query     string
	Relations []Relation
	TieTypes  []TieType
	Mode      SearchMode
	// Weights optionally scales a relation's contribution, in percent
	// (100 = neutral). A nil map uses neutral weights for every relation.
	Weights map[Relation]int
	Offset  int
	// Limit is the page size. <= 0 selects the server default (100); the
	// server clamps larger values to 1000.
	Limit int
	// Expand asks the server to attach each match's forward attributes.
	Expand bool
}

// SearchProfile is a reusable relation/type selection for one kind of app
// (audio browsing, file browsing, …).
type SearchProfile struct {
	Relations []Relation
	TieTypes  []TieType
	Weights   map[Relation]int
	Mode      SearchMode
}

// Spec builds a SearchSpec for query with the profile's defaults.
func (p SearchProfile) Spec(query string) SearchSpec {
	return SearchSpec{
		Query:     query,
		Relations: p.Relations,
		TieTypes:  p.TieTypes,
		Mode:      p.Mode,
		Weights:   p.Weights,
	}
}

// AudioSearchProfile is the vocabulary tie-audio searches: artist/album names
// and track titles, scoped to audio collections, audio files and audio
// archives. Album/artist matches live on track rows too, so callers should
// expect several rows per album and group them by member-of/parent.
var AudioSearchProfile = SearchProfile{
	Relations: []Relation{RelTitle, RelAlbum, RelAlbumArtist, RelArtist, RelFilename},
	TieTypes:  []TieType{TieAudioFile, TieAudioDir, TieAudioArchive},
	Weights: map[Relation]int{
		RelTitle:       150,
		RelAlbum:       120,
		RelAlbumArtist: 110,
		RelArtist:      100,
		RelFilename:    60,
	},
	Mode: SearchWords,
}

// FileSearchProfile is the vocabulary tie-fm searches: directory names and
// file names, scoped to directories and every file/archive type. It excludes
// "path" deliberately: a path hit on every descendant of a matching directory
// floods the result list, and the expanded attributes already carry the path
// for navigation.
var FileSearchProfile = SearchProfile{
	Relations: []Relation{RelFilename, RelName},
	TieTypes:  searchFileTypes(),
	Weights: map[Relation]int{
		RelFilename: 100,
		RelName:     100,
	},
	Mode: SearchWords,
}

// searchFileTypes returns the structural directory marker plus every file and
// archive tie-type. Directory labels (audio-dir, image-dir, …) are omitted: a
// directory always carries the structural "directory" marker as well.
func searchFileTypes() []TieType {
	out := make([]TieType, 0, TieFile-TieUnknownFile+1)
	for t := TieUnknownFile; t <= TieFile; t++ {
		if t >= TieImageDir && t <= TieDocumentDir {
			continue // labels, not the structural directory marker
		}
		out = append(out, t)
	}
	return out
}

// Search runs a free-text search and returns the ranked page, the total number
// of matching subjects before paging, and an error. No matches is a nil error
// with an empty result: an empty search is a normal UI state, not ErrNotFound.
func (tc *TieClient) Search(spec SearchSpec) ([]SearchMatch, int, error) {
	return tc.SearchContext(context.Background(), spec)
}

// SearchContext is Search carrying the caller's context, so a GUI that moved
// on from a query stops waiting instead of running it to completion (#114).
func (tc *TieClient) SearchContext(ctx context.Context, spec SearchSpec) ([]SearchMatch, int, error) {
	request := tc.CollectionInfo().NewSearchRequest()
	request.Query = spec.Query
	request.Relations = relationStrings(spec.Relations)
	request.TieTypes = tieTypeStrings(spec.TieTypes)
	request.Mode = spec.Mode
	request.Weights = relationWeights(spec.Weights)
	request.Offset = spec.Offset
	request.Limit = spec.Limit
	request.Expand = spec.Expand

	reply, err := callCtx[*api.SearchRequest, api.SearchReply](ctx, tc, request)
	if err != nil {
		return nil, 0, err
	}
	if e := replyError(reply.ReplyStatus); e != nil {
		return nil, 0, e
	}
	matches := make([]SearchMatch, 0, len(reply.Matches))
	for _, m := range reply.Matches {
		matches = append(matches, SearchMatch{
			Key:        m.Key,
			Relation:   Relation(m.Relation),
			Value:      m.Value,
			Score:      m.Score,
			Attributes: m.Attributes,
		})
	}
	return matches, reply.TotalCount, nil
}

func relationStrings(relations []Relation) []string {
	if len(relations) == 0 {
		return nil
	}
	out := make([]string, 0, len(relations))
	for _, r := range relations {
		if r != "" {
			out = append(out, string(r))
		}
	}
	return out
}

func tieTypeStrings(types []TieType) []string {
	if len(types) == 0 {
		return nil
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, t.String())
	}
	return out
}

func relationWeights(weights map[Relation]int) map[string]int {
	if len(weights) == 0 {
		return nil
	}
	out := make(map[string]int, len(weights))
	for relation, w := range weights {
		if relation != "" && w > 0 {
			out[string(relation)] = w
		}
	}
	return out
}

// SearchTimeout is a convenience bound for callers that do not already carry a
// context (a CLI one-shot, a script). GUI callers should cancel their own
// context when the query changes instead of using a fixed timeout.
const SearchTimeout = 30 * time.Second
