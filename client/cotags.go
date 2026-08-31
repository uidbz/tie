package client

import (
	"errors"

	"github.com/uidbz/tie/api"
)

// CoTagsForQuery returns all unique tags carried by entries that match ALL of
// include (AND) and NONE of exclude, optionally scoped to a tie-type value.
//
// This is the "faceted refinement" (narrowing) query: given a user's current
// tag filter, it answers "what further tags can I narrow by?" For example,
// given include=["tree","nature"] it returns tags such as ["2026","norway",
// "sunset"] that appear on files tagged with both "tree" and "nature".
//
// The returned slice is sorted and deduplicated. Returns ErrNotFound (with a
// nil slice) when no entries match the given include terms.
func (tc *TieClient) CoTagsForQuery(include, exclude []string, scope string) ([]string, error) {
	col := tc.collectionInfo("")
	request := col.NewCoTagsRequest(include, exclude, scope)

	reply, err := run[api.CoTagsReply](tc, request)
	if err != nil {
		return nil, err
	}
	if e := replyError(reply.ReplyStatus); e != nil {
		return nil, e
	}
	return reply.Tags, nil
}

// CoTagsForQueryExcludingInput is a convenience wrapper around CoTagsForQuery
// that removes the input include tags from the returned set. Use this when
// building a "refine further" UI where the seed tags are already selected and
// should not appear again as options.
func (tc *TieClient) CoTagsForQueryExcludingInput(include, exclude []string, scope string) ([]string, error) {
	tags, err := tc.CoTagsForQuery(include, exclude, scope)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inputSet := make(map[string]bool, len(include))
	for _, t := range include {
		inputSet[t] = true
	}
	out := tags[:0:len(tags)]
	for _, t := range tags {
		if !inputSet[t] {
			out = append(out, t)
		}
	}
	return out, nil
}
