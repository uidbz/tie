# Free-text search

`tie` is a triple store: association lookups are exact. There is no substring,
token or n-gram index on disk, and nothing in the record format stores one. This
document describes the search layer that adds free-text lookup without changing
the data model: how it works, what it guarantees, how fast it is, and how the
CLI and GUI apps use it.

## TL;DR

- New read request **`Search`**, new `client.Search`, new `tie search`.
- Matching is case-insensitive and accent-folded.
- Default mode is **`words`**: every query token must match somewhere on the
  subject, **different fields allowed** (`beatles abbey` finds *The Beatles /
  Abbey Road*).
- Callers name the searchable relations and (optionally) the tie-types.
- The server builds a **transient in-memory corpus** from the existing index,
  caches it per `(relations, tie-types)`, and invalidates it on writes.
- **No on-disk format change, no migration, no new records.**
- The corpus build reads **only RAM**: no record-file I/O, no blob downloads, no
  writer-queue traffic.

## Why not an on-disk search index?

There are three obvious alternatives, all rejected for the first implementation:

| Approach | Why not |
|---|---|
| Materialise tokens/trigrams as triples (`(subject, "search-gram", "abe")`) | Every searchable string would add many triples, each with forward and reverse association memory. On a media library that is a large permanent cost for an interactive feature, and it changes the write workload and file size. |
| A persistent sidecar index (`<collection>.tie.search`) | A second file to lock, back up, verify, recover and keep in sync with `restore`/`drop`/`-f`/read-only. It also needs its own format and migration story. |
| SQLite FTS5 / Bleve | A large dependency (or cgo) in the triplestore, which is otherwise a small, single-file, pure-Go service. |

The existing index has properties that make a transient corpus cheap:

1. Values are interned; entry IDs are globally unique and never reused.
2. `levelOf(id)` finds an entry's trie level with a handful of in-memory map
   lookups (the same helper `CheckIndex` uses).
3. `getValueString(level, id)` reconstructs the string from the trie.
4. The forward association sets are already in memory in both modes.

So the corpus can be derived from RAM and thrown away without touching disk. A
persistent index remains the escalation path if a future library outgrows the
in-memory approach (see [Limits and future work](#limits-and-future-work)).

## Request semantics

### Relations

`Relations` names the predicates to search. The engine does not invent a
vocabulary: an empty list returns no matches. This prevents a typo or a
generic caller from accidentally indexing every value in the collection.

Typical sets:

| App | Relations |
|---|---|
| tie-audio | `title`, `album`, `album-artist`, `artist`, `genre`, `tag`, `filename` |
| tie-fm | `filename`, `name` |
| Ad hoc | `tag`, `genre`, any relation the collection uses |

`path` is intentionally **not** in the file profile: matching a directory path
would return every descendant, flooding the result list. The expanded
attributes already carry `path` for navigation.

### Tie-types

`TieTypes` restricts candidates to subjects carrying at least one of the named
`tie-type` values (`audio-file`, `audio-dir`, `directory`, `file`, …). Custom
dir-type labels (`live-album`, `playlist`, a site-specific label) are valid
`tie-type` values too; the Go client passes them through `SearchSpec.TieTypeNames`
and `tie search --type` accepts any label registered under `(types, all, …)`.

When `tie-type` is in the collection's reverse index (the default), candidate
subjects are looked up directly with `GetReverseAssociations(type)`, so a search
over audio files never walks unrelated subjects. If a server was configured with
a reverse-relation set that omits `tie-type`, search falls back to a full index
walk and filters by direct forward-set membership; results are identical, the
walk is just wider.

An empty `TieTypes` means "all subjects".

### Modes

| Mode | Semantics | Example |
|---|---|---|
| `words` (default) | Split the query into tokens; every token must match somewhere on the subject. Tokens may match different fields. | `beatles abbey` → artist `The Beatles`, album `Abbey Road` |
| `substring` | The whole query must appear in one field, in order. | `abbey ro` |
| `prefix` | Query tokens must match the start of a token/field. No infix, no typo. | `nev` → `Nevermind` |
| `fuzzy` | `words`, plus bounded Levenshtein for tokens no exact/prefix/substring match satisfies. | `beetles` → `Beatles` |

All modes fold case and accents (`Beyoncé` matches `beyonce`), drop apostrophes
(`Don't` tokenises as `dont`), and split on every other non-letter/non-digit
rune.

### Subject-level matching

Matching is **per subject**, not per field. For a subject, each query token is
scored against all of its searchable fields; the best match for each token is
kept, and the subject is a hit only when every token found a home. This is what
makes a music search feel natural: an artist token and an album token can be
satisfied by different fields on the same track or album row.

The hit's `relation`/`value` report the field that contributed most to the
score.

### Scoring

Scores are integers, so two runs of one query produce byte-identical ordering.
They are not probabilities; only relative order matters.

| Match | Base |
|---|---|
| Field equals the whole query (`substring`) | 1000 |
| Field starts with the whole query (`substring`) | 850 |
| Whole query at a word boundary (`substring`) | 800 |
| Whole query anywhere in a field (`substring`) | 600 |
| Token equals a candidate token (`words`/`prefix`/`fuzzy`) | 800 |
| Candidate token starts with the query token | 700 |
| Candidate token contains the query token | 600 |
| Field contains the query token across token boundaries | 500 |
| Fuzzy distance 1 | 500 |
| Fuzzy distance 2 | 400 |

The whole-query rows are used by `substring` mode. The other modes score each
query token and sum the per-token bests.

Then:

- A field containing the complete query gets a `+250` phrase bonus (`words`,
  `prefix`, `fuzzy`).
- An optional per-relation weight (percent) scales a field's contribution.
- A small length bonus prefers `Rock` over a long title beginning with `Rock`.
- Ties break deterministically: relation ascending, then value, then subject key.
  The representative relation/value of a hit uses the same tie-break, so a
  corpus rebuild cannot reorder equal-scoring hits.

### Limits and empty results

| Dimension | Default | Maximum |
|---|---|---|
| Query length | — | 256 runes |
| Query tokens | — | 16 |
| Relations | — | 16 |
| Tie-types | — | 32 |
| Relation/type/weight name | — | 256 bytes |
| Field value length | — | 1024 bytes |
| Page size | 100 | 1000 |
| Offset | 0 | 100000 |
| Cached corpora per collection | — | 16 |

- An empty query, a punctuation-only query, or an empty relation list is a
  **successful empty result**, not `ErrNotFound`. Search is a UI primitive;
  "nothing typed yet" is normal.
- No matches is likewise a successful empty result (exit 0 in the CLI).
- `TotalCount` is the exact number of matching subjects before paging.
- `Revision` is the index generation the page came from. If a client pages
  across writes and the revision changes, it can restart the query.

## How the server builds and caches the corpus

The engine lives in `tiedb/search.go` (corpus, ranking) and
`tiedb/search_match.go` (folding, tokenisation, scoring, edit distance).

### Corpus shape

```go
type searchCorpus struct {
    revision uint64
    key      string
    subjects []*searchSubject
    bytes    int64
    lastUse  uint64 // LRU, guarded by searchMu
}

type searchSubject struct {
    key    string
    fields []searchField
}

type searchField struct {
    relation string
    value    *searchValue
}

type searchValue struct {
    text   string
    norm   string   // folded
    tokens []string
}
```

- One entry per subject; one field per `(relation, value)` pair the subject
  holds under the requested relations.
- Identical value text is shared via one `*searchValue`, so an artist or album
  repeated across thousands of tracks is stored once.
- The corpus is immutable after construction; concurrent readers need no lock.

### Build

1. Resolve the requested relation names to entry IDs.
2. Resolve candidate subjects:
   - **Narrowed:** `GetReverseAssociations(tieType)` for each requested type,
     keeping only entries whose relation is `tie-type` itself.
   - **Full scan:** every forward association tree, optionally filtered by
     direct membership of `(subject, tie-type, type)`.
3. For each subject, snapshot its forward association entries, then resolve the
   requested relations' values with `levelOf` + `getValueString`.
4. Skip empty values and values over 1024 bytes.

`AssociationSet.ForEach` may hold a shard read lock across its callback, so the
build snapshots `(valueID, relationID)` pairs first and resolves them after the
callback returns. This follows the iteration rules in `tiedb/doc.go` and never
nests an association-set lock inside an entry-tree lookup.

### Invalidation

`Collection.searchGen` is an atomic counter bumped by the user-write paths:

- `addLocked` (every successful `Add`, `Update`, `Set` and batch add),
- `deleteLocked` (every forward removal; reverse-only cleanup does not count),
- `CheckIndex` when a repair actually changes the index.

The bulk load inserts entries and associations without bumping: a collection is
not served until the load finishes, so no corpus can exist at generation 0.

A corpus carries the generation it was built at. `Search` reuses it only if the
generation still matches; otherwise it rebuilds. Corpora with an older
generation are dropped as soon as a search observes the new one, and a write
that lands during a build prevents that corpus from being installed, so a later
query cannot serve stale data. The in-flight query itself is a snapshot, exactly
like a paged `Query` held across a write.

### Memory

The cache is per collection, keyed by `(relations, tie-types)`. tie-audio and
tie-fm therefore keep separate corpora with no wasted fields. The default budget
is **64 MiB per collection**:

```toml
# tie-triplestore.toml
SearchCacheMaxMB = 64   # 0 or omitted selects the 64 MiB default
DisableSearchCache = false
```

The budget is an estimate (`len(text) + len(norm) + tokens*16 + struct
overhead`), plus the selection key itself, and eviction is least-recently-used
across corpora. At most 16 corpora are retained, so empty selections cannot
accumulate. A corpus larger than the whole budget is used for the request and
not installed. With `DisableSearchCache = true`, every search rebuilds the
corpus — correct, just slower; useful on a memory-constrained host.

### Measured performance

Synthetic benchmark (`tiedb/search_bench_test.go`, AMD Ryzen 9 9950X, one run):

| Workload | Time | Notes |
|---|---|---|
| Cold build + query, 1k subjects | ~1.8 ms | corpus build dominates |
| Cold build + query, 10k subjects | ~18 ms | |
| Cold build + query, 100k subjects | ~223 ms | corpus build dominates |
| Warm query, 10k subjects | ~1.2 ms | |
| Warm query, 100k subjects | ~15 ms | 4 searchable fields each |
| Warm fuzzy query, 50k subjects | ~18 ms | |

The corpus build is RAM-only. On a bigger library the build is the cost to
watch; warm queries scale with the number of indexed fields, not the raw triple
count. Run the benchmarks on your own hardware with:

```sh
go test ./tiedb -run '^$' -bench 'BenchmarkSearch' -benchtime=3x
```

## Protocol

### Request

```json
{
  "Id": "Search",
  "Namespace": "Collections",
  "CollectionId": "Main",
  "query": "beatles abbey",
  "relations": ["artist", "album", "album-artist", "title", "filename"],
  "tieTypes": ["audio-file", "audio-dir", "audio-archive"],
  "mode": "words",
  "weights": {"title": 150, "album": 120},
  "offset": 0,
  "limit": 100,
  "expand": true
}
```

`Search` is registered as a **read** request:

- A read-role user may call it.
- A read-only database (`tie --read-only`, `-f` read-only, read-only SMB
  sessions) serves it.
- It is in the client's `readOnlyRequests` retry allowlist, so a transient
  transport failure is retried like `Query`.

### Reply

```json
{
  "Success": true,
  "matches": [
    {
      "key": "sha256-hex-or-dir-uid",
      "relation": "album",
      "value": "Abbey Road",
      "score": 1480,
      "attributes": {"album": ["Abbey Road"], "artist": ["The Beatles"], "...": []}
    }
  ],
  "totalCount": 37,
  "revision": 12345
}
```

- `attributes` is present only with `"expand": true`.
- One match per subject. `totalCount` counts subjects, not triples.
- Matches are ordered best-first, deterministically.

### Compatibility

This is an additive request. Old clients are unaffected. A new client talking to
an old triplestore gets `404 Not Found` for `Search`; surface it as "the
triplestore does not support Search; upgrade it". Apps may fall back to
filtering already-loaded data (tie-audio's album wall is a natural case).

## Go client

```go
spec := client.AudioSearchProfile.Spec("beatles abbey")
spec.Limit = 200
spec.Expand = true

matches, total, err := tie.Search(spec)
if err != nil { /* transport or server error */ }
for _, m := range matches {
    fmt.Println(m.Key, m.Relation, m.Value, m.Score)
    if albumUIDs := client.RowValues(client.Row{Attributes: m.Attributes}, client.RelMemberOf); len(albumUIDs) > 0 {
        // group a track hit into its album
    }
}
```

- `SearchContext` is the cancellable form; GUIs should cancel the in-flight
  query when the user types again.
- `SearchPage`/`SearchPageContext` return `SearchResult{Matches, Total,
  Revision}`. Use them when paging a live collection: if page two reports a
  different `Revision` than page one, a write landed in between and the caller
  should restart the query. `Search` keeps the old three-value signature and is
  a wrapper around `SearchPage`.
- `SearchWords/Substring/Prefix/Fuzzy` are re-exported, as are `SearchMatch`,
  `SearchResult` and `SearchSpec`.
- `AudioSearchProfile` and `FileSearchProfile` are the shared vocabularies.
  They are plain values; copy and adjust the relation/type lists for an app's
  own collection. `SearchSpec.TieTypeNames`/`SearchProfile.TieTypeNames` carry
  custom dir-type labels the `TieType` enum cannot express.

Building a raw spec:

```go
matches, total, err := tie.Search(client.SearchSpec{
    Query:     "notes",
    Relations: []client.Relation{client.RelFilename, client.RelName},
    TieTypes:  []client.TieType{client.TieDirectory, client.TieFile},
    Mode:      client.SearchWords,
    Weights:   map[client.Relation]int{client.RelFilename: 120},
    Limit:     100,
    Expand:    true,
})
```

## CLI

`tie search` requires at least one `--relation`; it deliberately has no default
vocabulary. `--type` accepts a built-in tie-type name or any custom dir-type
label registered in the collection.

```sh
tie search "abbey road" --relation album --relation title
tie search beatles --relation artist --relation album-artist --type audio-file
tie search cover --relation filename --relation name --type directory --type file
tie search remaster --relation album --type live-album
tie search abbey --relation album --mode substring
tie search beet --relation artist --mode fuzzy --limit 20
tie search jazz --relation tag --relation genre
tie search abbey --relation album --weight album=150 --format json
```

Columns: `key`, `relation`, `value`, `score`. No matches prints nothing and
exits 0. `--format json` emits one object per match.

Useful combinations:

- `--type` is what keeps a search cheap; without it the engine may fall back to
  a full subject walk.
- `--weight relation=percent` tunes ranking, e.g. `--weight title=150`.
- `-o/--offset` and `-l/--limit` page a large result list. The exact total is
  printed on stderr when stdout is a terminal and the page is truncated; a
  script that needs the total or the revision (to detect a write between pages)
  should call `client.SearchPage`, whose `SearchResult` carries both.

## tie-audio and tie-fm integration

The apps use the same profiles and add only presentation logic.

### tie-audio

1. `Session.Search` calls `client.AudioSearchProfile.Spec(query)` with
   `Expand: true` and a generous page (500) so album grouping sees a
   representative set. The profile also searches `genre` and `tag`, so a query
   like `jazz` surfaces tagged/genre-matched albums without a separate tag
   query.
2. Album/artist/title hits are expanded. Track hits contribute their
   `member-of` (preferred) or `parent` album UID; those album rows are fetched
   with one `Expand` and classified with the same row classifier the album wall
   uses.
3. Results feed the existing cover wall/table as album tiles; standalone tracks
   appear as track tiles, a shape the wall already supports.
4. The search field debounces (~175 ms), requires two characters, cancels the
   previous context, and shows a spinner during the first corpus build.
5. The current tag selection is kept as a separate concept: search covers the
   whole library by default, and the UI offers "search in current selection" by
   filtering the result set against the albums already on screen.

### tie-fm

1. `TieFS.Search` uses `client.FileSearchProfile` with `Expand: true` and maps
   each row to an `Entry`: name from `filename`/`name`, path from `path`,
   `IsDir` from `tie-type`, size/date from `filesize`/`tag-date`.
2. The file manager shows a temporary "Results for …" view in its existing
   table. Double-clicking a file navigates to `path.Dir(path)`; double-clicking
   a directory navigates into it; Back clears the search.
3. Search is tie-only for now; local filesystem search is a separate backend
   decision (a `filepath.WalkDir` implementation can reuse the same view).

## Limits and future work

The transient corpus is the right trade-off for personal libraries, but it has
a known ceiling: warm query cost grows with the number of indexed fields, and a
collection far beyond a million searchable fields will want a persistent index.

If that day comes, the escalation path is a sidecar inverted index behind the
same `Search` API:

- Build it lazily from the same corpus walk and persist it as
  `<collection>.tie.search`.
- Invalidate by the same generation counter, persisted in a footer.
- Keep the in-memory path as the fallback when the sidecar is missing, stale or
  unwritable.
- Do not change the `.tie` record format; a search index is derived data, and
  `verify` can rebuild it.

Other deliberate non-goals for now:

- Boolean query syntax (`AND`/`OR`/`-term`), field-qualified queries
  (`artist:beatles`), and quoted phrases. The relation list and modes already
  cover the app needs; a parser can be added above the engine later.
- Cross-subject relevance learning or click feedback.
- Searching inside blob contents. Search indexes metadata values only; content
  search belongs to a different service (and would need extraction).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `404` on Search | Old triplestore. Upgrade it; the request is additive. |
| Everything matches, ranking feels noisy | Too many relations, or a broad relation such as `path`/`tag`. Narrow with `--type` and specific relations. |
| A new import is not findable immediately | The corpus is rebuilt on the first search after a write; it should be immediate. Check that the relation you search is actually written (e.g. `album` may live only on track rows). |
| Search uses too much memory | Lower `SearchCacheMaxMB`, or set `DisableSearchCache = true`. |
| First search after a restart is slow on a huge collection | Expected: the corpus is derived lazily. It is RAM-only; subsequent queries reuse it until a write. |
| `--type` search misses files | The subjects may lack a `tie-type` triple. Check with `tie get <key>`; search filters by the type values, not by file extensions. |
