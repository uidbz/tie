package client

// history.go records media consumption (plays of tracks, episodes, movies)
// as first-class play-event subjects and keeps per-item statistics
// denormalized onto the played items and their collections.
//
// One play is one subject:
//
//	(ev, tie-type,      play-event)
//	(ev, play-of,       <content hash>)        what was played
//	(ev, play-in,       <collection uid>)      album / playlist / series (optional)
//	(ev, played-at,     RFC3339 UTC)           when playback of the item started
//	(ev, listened,      <seconds>)             time actually heard (seeks excluded)
//	(ev, play-duration, <seconds>)             the item's length
//	(ev, outcome,       complete|skip)
//	(ev, play-source,   tie-audio@host | pwplay@host …)
//	(ev, play-context,  album|playlist|home|queue|shuffle …) (optional)
//	(ev, play-position, <seconds>)             where playback stopped (video resume)
//
// The event id is derived from (source, sequence number) when the source
// numbers its events, so re-delivering the same play — a client draining a
// server's play log twice, a retried spool flush — writes the same subject
// and is detected and skipped. The events are the source of truth; the
// statistics on items (play-count, skip-count, first-played, last-played,
// last-skipped) are a cache for one-round-trip listings and can be rebuilt
// with RecomputeStats.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/uidbz/tie/api"
)

// Play-event relations.
const (
	RelPlayOf       = "play-of"
	RelPlayIn       = "play-in"
	RelPlayedAt     = "played-at"
	RelListened     = "listened"
	RelPlayDuration = "play-duration"
	RelOutcome      = "outcome"
	RelPlaySource   = "play-source"
	RelPlayContext  = "play-context"
	RelPlayPosition = "play-position"
)

// PlayOutcome classifies a play.
type PlayOutcome string

const (
	// OutcomeComplete is a play that counts: the item was heard for at least
	// half its length or four minutes (see IsCompletePlay).
	OutcomeComplete PlayOutcome = "complete"
	// OutcomeSkip is a play abandoned early; a negative signal for
	// recommendations.
	OutcomeSkip PlayOutcome = "skip"
)

// Play-counting thresholds (the scrobbling convention).
const (
	MinPlayableDuration = 30.0  // seconds; shorter items never count as played
	CompletePlayFloor   = 240.0 // seconds heard that always count as complete
	SkipCeiling         = 30.0  // heard less than this (or < 25%) = skip
)

// ClassifyPlay applies the counting rule to a finished play: complete when
// the item is longer than 30 s and was heard for at least half its length or
// 4 minutes; skip when it was heard for less than 30 s or less than a quarter
// of its length; otherwise neither (ok == false: not worth recording).
func ClassifyPlay(listened, duration float64) (PlayOutcome, bool) {
	if duration > MinPlayableDuration && (listened >= duration/2 || listened >= CompletePlayFloor) {
		return OutcomeComplete, true
	}
	if duration <= 0 {
		if listened >= CompletePlayFloor {
			return OutcomeComplete, true
		}
		return "", false
	}
	if listened < SkipCeiling || listened < duration/4 {
		return OutcomeSkip, true
	}
	return "", false
}

// PlayEvent is one play.
type PlayEvent struct {
	// ID is the event subject. Empty: derived from Source+Seq when Seq > 0,
	// else random.
	ID         string
	Source     string
	Seq        uint64
	Subject    string // content hash of the played item
	Collection string // album / playlist / series uid ("" = unknown)
	PlayedAt   time.Time
	Listened   float64
	Duration   float64
	Outcome    PlayOutcome
	Context    string
	Position   float64
}

// PlayEventID derives the subject id of the seq-th event of source: a 64-hex
// digest (the same shape as content hashes and DirUIDs).
func PlayEventID(source string, seq uint64) string {
	sum := sha256.Sum256([]byte("play-event\x00" + source + "\x00" + strconv.FormatUint(seq, 10)))
	return hex.EncodeToString(sum[:])
}

func (e *PlayEvent) id() string {
	if e.ID != "" {
		return e.ID
	}
	if e.Seq > 0 && e.Source != "" {
		e.ID = PlayEventID(e.Source, e.Seq)
		return e.ID
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("client: reading random bytes for play event: " + err.Error())
	}
	e.ID = hex.EncodeToString(b[:])
	return e.ID
}

// PlayStats are the denormalized statistics of one item or collection.
type PlayStats struct {
	PlayCount   int
	SkipCount   int
	FirstPlayed time.Time
	LastPlayed  time.Time
	LastSkipped time.Time
}

// StatsFromRow reads the statistics relations off an expanded row.
func StatsFromRow(r Row) PlayStats {
	var s PlayStats
	s.PlayCount, _ = strconv.Atoi(RowFirst(r, RelPlayCount))
	s.SkipCount, _ = strconv.Atoi(RowFirst(r, RelSkipCount))
	s.FirstPlayed, _ = ParseMediaTime(RowFirst(r, RelFirstPlayed))
	s.LastPlayed, _ = ParseMediaTime(RowFirst(r, RelLastPlayed))
	s.LastSkipped, _ = ParseMediaTime(RowFirst(r, RelLastSkipped))
	return s
}

func (s *PlayStats) apply(e PlayEvent) {
	t := e.PlayedAt.UTC().Truncate(time.Second)
	switch e.Outcome {
	case OutcomeComplete:
		s.PlayCount++
		if s.FirstPlayed.IsZero() || t.Before(s.FirstPlayed) {
			s.FirstPlayed = t
		}
		if t.After(s.LastPlayed) {
			s.LastPlayed = t
		}
	case OutcomeSkip:
		s.SkipCount++
		if t.After(s.LastSkipped) {
			s.LastSkipped = t
		}
	}
}

func (s PlayStats) relations() map[Relation][]string {
	out := map[Relation][]string{}
	if s.PlayCount > 0 {
		out[RelPlayCount] = []string{strconv.Itoa(s.PlayCount)}
		out[RelFirstPlayed] = []string{FormatMediaTime(s.FirstPlayed)}
		out[RelLastPlayed] = []string{FormatMediaTime(s.LastPlayed)}
	}
	if s.SkipCount > 0 {
		out[RelSkipCount] = []string{strconv.Itoa(s.SkipCount)}
		out[RelLastSkipped] = []string{FormatMediaTime(s.LastSkipped)}
	}
	return out
}

// PlayEventFromRow parses an expanded play-event row.
func PlayEventFromRow(r Row) PlayEvent {
	e := PlayEvent{
		ID:         r.Key,
		Source:     RowFirst(r, RelPlaySource),
		Subject:    RowFirst(r, RelPlayOf),
		Collection: RowFirst(r, RelPlayIn),
		Outcome:    PlayOutcome(RowFirst(r, RelOutcome)),
		Context:    RowFirst(r, RelPlayContext),
	}
	e.PlayedAt, _ = ParseMediaTime(RowFirst(r, RelPlayedAt))
	e.Listened, _ = strconv.ParseFloat(RowFirst(r, RelListened), 64)
	e.Duration, _ = strconv.ParseFloat(RowFirst(r, RelPlayDuration), 64)
	e.Position, _ = strconv.ParseFloat(RowFirst(r, RelPlayPosition), 64)
	return e
}

const historyFlush = 1000

// RecordPlays writes play events and updates the statistics of every played
// item and collection, in a few round trips: one Expand to drop events that
// are already recorded (idempotent re-delivery), one Expand of the touched
// items' current statistics, then batched writes. It returns how many events
// were new. Events without a Subject or Outcome are rejected.
func (tc *TieClient) RecordPlays(events []PlayEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(events))
	byID := map[string]PlayEvent{}
	for i := range events {
		e := events[i]
		if e.Subject == "" || (e.Outcome != OutcomeComplete && e.Outcome != OutcomeSkip) {
			return 0, fmt.Errorf("record plays: event %d: need a subject and an outcome", i)
		}
		if e.PlayedAt.IsZero() {
			e.PlayedAt = time.Now()
		}
		id := e.id()
		if _, dup := byID[id]; dup {
			continue
		}
		byID[id] = e
		ids = append(ids, id)
	}
	existing, err := tc.Expand(ids)
	if err != nil {
		return 0, fmt.Errorf("record plays: %w", err)
	}
	for _, r := range existing {
		if RowHas(r, RelType, TypePlayEvent) {
			delete(byID, r.Key)
		}
	}
	if len(byID) == 0 {
		return 0, nil
	}
	fresh := make([]PlayEvent, 0, len(byID))
	for _, e := range byID {
		fresh = append(fresh, e)
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].PlayedAt.Before(fresh[j].PlayedAt) })

	subjects := map[string]bool{}
	for _, e := range fresh {
		subjects[e.Subject] = true
		if e.Collection != "" {
			subjects[e.Collection] = true
		}
	}
	keys := make([]string, 0, len(subjects))
	for k := range subjects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows, err := tc.Expand(keys)
	if err != nil {
		return 0, fmt.Errorf("record plays: %w", err)
	}
	stats := map[string]*PlayStats{}
	for _, k := range keys {
		stats[k] = &PlayStats{}
	}
	for _, r := range rows {
		s := StatsFromRow(r)
		stats[r.Key] = &s
	}

	if err := tc.EnsureMediaSchema(); err != nil {
		return 0, err
	}
	b := tc.NewBatch()
	flush := func() error {
		if len(b.Ops) == 0 {
			return nil
		}
		if _, err := tc.Batch(b); err != nil {
			return err
		}
		b = tc.NewBatch()
		return nil
	}
	for _, e := range fresh {
		appendPlayEventOps(b, e)
		stats[e.Subject].apply(e)
		if e.Collection != "" {
			stats[e.Collection].apply(e)
		}
		if len(b.Ops) >= historyFlush {
			if err := flush(); err != nil {
				return 0, fmt.Errorf("record plays: %w", err)
			}
		}
	}
	for _, k := range keys {
		for rel, vals := range stats[k].relations() {
			b.Set(k, string(rel), vals)
		}
	}
	if err := flush(); err != nil {
		return 0, fmt.Errorf("record plays: %w", err)
	}
	return len(fresh), nil
}

func appendPlayEventOps(b *api.Batch, e PlayEvent) {
	id := e.ID
	b.Add(id, string(RelType), TypePlayEvent)
	b.Add(id, string(RelPlayOf), e.Subject)
	if e.Collection != "" {
		b.Add(id, string(RelPlayIn), e.Collection)
	}
	b.Set(id, string(RelPlayedAt), []string{FormatMediaTime(e.PlayedAt)})
	b.Set(id, string(RelListened), []string{strconv.FormatFloat(e.Listened, 'f', 1, 64)})
	if e.Duration > 0 {
		b.Set(id, string(RelPlayDuration), []string{strconv.FormatFloat(e.Duration, 'f', 1, 64)})
	}
	b.Set(id, string(RelOutcome), []string{string(e.Outcome)})
	if e.Source != "" {
		b.Set(id, string(RelPlaySource), []string{e.Source})
	}
	if e.Context != "" {
		b.Set(id, string(RelPlayContext), []string{e.Context})
	}
	if e.Position > 0 {
		b.Set(id, string(RelPlayPosition), []string{strconv.FormatFloat(e.Position, 'f', 1, 64)})
	}
}

// PlayQuery selects play events for RecentPlays.
type PlayQuery struct {
	Since time.Time // zero = no lower bound
	Limit int       // <= 0 = all
	// Outcome restricts to one outcome ("" = both).
	Outcome PlayOutcome
}

// RecentPlays returns play events newest first, in one round trip
// (server-side sort on played-at; the fixed-width UTC format sorts
// chronologically as strings).
func (tc *TieClient) RecentPlays(q PlayQuery) ([]PlayEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, _, err := tc.Query(QuerySpec{
		Terms: []string{TypePlayEvent}, Filter: string(RelType), Reverse: true, Expand: true,
		SortByValue: RelPlayedAt, Descending: true, Limit: limit,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]PlayEvent, 0, len(rows))
	for _, r := range rows {
		e := PlayEventFromRow(r)
		if !q.Since.IsZero() && e.PlayedAt.Before(q.Since) {
			continue
		}
		if q.Outcome != "" && e.Outcome != q.Outcome {
			continue
		}
		out = append(out, e)
	}
	// The server sort is advisory for rows lacking the relation; enforce it.
	sort.SliceStable(out, func(i, j int) bool { return out[i].PlayedAt.After(out[j].PlayedAt) })
	return out, nil
}

// RecomputeStats rebuilds play-count/skip-count/first-/last-played on every
// item and collection that has play events, from the events themselves. It
// repairs counters that lost an update (two devices recording at once) and
// returns the number of subjects written.
func (tc *TieClient) RecomputeStats() (int, error) {
	events, err := tc.RecentPlays(PlayQuery{})
	if err != nil {
		return 0, err
	}
	stats := map[string]*PlayStats{}
	get := func(k string) *PlayStats {
		if s, ok := stats[k]; ok {
			return s
		}
		s := &PlayStats{}
		stats[k] = s
		return s
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		get(e.Subject).apply(e)
		if e.Collection != "" {
			get(e.Collection).apply(e)
		}
	}
	if err := tc.EnsureMediaSchema(); err != nil {
		return 0, err
	}
	b := tc.NewBatch()
	for k, s := range stats {
		rels := s.relations()
		for _, rel := range []Relation{RelPlayCount, RelSkipCount, RelFirstPlayed, RelLastPlayed, RelLastSkipped} {
			b.Set(k, string(rel), rels[rel]) // nil clears a relation the events no longer support
		}
		if len(b.Ops) >= historyFlush {
			if _, err := tc.Batch(b); err != nil {
				return 0, err
			}
			b = tc.NewBatch()
		}
	}
	if len(b.Ops) > 0 {
		if _, err := tc.Batch(b); err != nil {
			return 0, err
		}
	}
	return len(stats), nil
}
