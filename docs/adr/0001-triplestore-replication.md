# ADR 0001: Triplestore replication across instances

- Status: Proposed
- Issue: #161
- Related: #159 (filehost replication primitives), #160 (resumable uploads)

## Context

A deployment with several sites wants each site to read and write locally,
and to move data between sites. Blobs and triples need different treatment:

- **Blobs** (tie-filehost) are immutable and content-addressed. Replicating
  them is a set difference plus ordinary uploads; #159 adds the read-side
  primitives (`GET /-/keys/{store}`, `POST /-/has`). Two sites can never
  disagree about what a blob contains.
- **Triples** (tie-triplestore, `tiedb`) are mutable. `Update` is a
  delete+add inside one critical section, deletes write tombstones, and
  freed slots are reused (`tiedb` record file + freespace stack). Two sites
  can disagree: one renames an album while the other tags it; one deletes
  a `cover` relation while the other replaces it.

So the record file is **not** an append-only log today. Slots are rewritten,
which means it cannot be shipped and replayed as-is. Any replication scheme
needs either an explicit operation log or read-time merging.

The question this ADR answers is the one in #161: does "replicated
filehost" mean replicated *store* (blobs and the metadata that names them)
or only replicated *blobs*? The answer determines whether a site can work
when another site is unreachable.

## Options

### 1. Per-site triplestores with query-time fan-out (federation)

Each site runs its own triplestore, which is authoritative for the triples
written there. Readers query the local store and, when they need the global
view, fan out to the other sites and merge the results.

- (+) No replication protocol and no new storage format. Writes are always
  local and never block on the WAN.
- (+) A partition degrades to "only local data" and is never inconsistent.
- (−) A global read costs a WAN round trip per site, and its availability is
  that of the slowest site. `Search` and `CoTags` have to merge ranked or
  aggregated results across stores. Pagination and dedup across sites are
  new client logic in every reader (CLI, FUSE mount, SMB gateway).
- (−) The same subject annotated at two sites gives two partial truths. A
  delete at one site does not remove a triple held by another, so "delete
  the cover" means "delete it everywhere you can reach".
- (−) Directory UIDs and retention-related relations that callers treat as
  single-valued (`cover`, `thumbnail`) become multi-valued across sites.

### 2. Primary + append-only operation log, read replicas

One triplestore is the primary for a collection. Every committed mutation
is also appended to an operation log: an ordered, idempotent `add` /
`delete` / `update` record with a monotonically increasing sequence number.
Replicas at other sites start from a snapshot (`Dump`) and then tail the
log. They serve reads locally and forward writes to the primary.

- (+) One answer for every triple. Single-valued relations stay
  single-valued, and deletes propagate.
- (+) Local reads at every site. The log is the replication unit, so a
  replica that was offline catches up from its last sequence number, or
  from a fresh snapshot when the log has been compacted past it.
- (+) The log is useful on its own: audit trail, point-in-time recovery,
  and a change feed for derived indexes (search corpus, media DB).
- (−) Writes from non-primary sites pay a WAN round trip, and fail while
  the primary is unreachable (read-only degradation).
- (−) New code: the log writer in `tiedb`, which must sit in the same
  critical section as the mutation so log order equals commit order; log
  retention/compaction; a replica apply loop; a snapshot-plus-sequence
  handshake.

### 3. Multi-primary with conflict resolution (CRDT/LWW) — rejected

Every site writes locally and merges logs from the others, using
last-writer-wins per (subject, relation) or set CRDTs. This is rejected
for now. Last-writer-wins silently drops concurrent edits to single-valued
relations, set CRDTs change the meaning of `Update`, and both need hybrid
clocks across sites. None of the current workloads needs offline multi-site
writes enough to pay for that.

## Decision (proposed)

Adopt **option 2** for collections that more than one site must see, and
keep **blob replication separate** (#159):

1. "Replicated filehost" means replicated **blobs**. Blob sync uses the
   #159 primitives and does not depend on the triplestore.
2. Triples have one primary per collection. Replicas tail an operation log
   and are read-only. Writes go to the primary.
3. Site-local collections (scratch annotations, caches) need no
   replication and stay on their local store. That is option 1 restricted
   to data that no other site needs.

## Consequences

- `tiedb` gains an operation log: one record per committed mutation,
  written under `changeMutex` together with the mutation and
  fsync-batched with the record file. The on-disk format of the record
  file is unchanged. The log is a new file per collection, versioned, with
  a CHANGELOG entry.
- tie-triplestore gains `GET /-/log?after=<seq>` (streaming) and a
  snapshot endpoint that returns a `Dump` plus the sequence number it
  reflects. A replica mode applies the log through the same `*Locked`
  helpers that local writes use, so indexes stay consistent.
- Clients need a "write endpoint" distinct from the "read endpoint". The
  webservice client already separates read-only request IDs
  (`readOnlyRequests`), which is the natural switch.
- Filehost retention is not a triple. It lives in each store's retention
  index and travels with uploads (`Tie-Retention`). A replicator must set
  it on the target explicitly; the #159 docs say so.
- Reaper safety across sites: a blob referenced only by triples at another
  site can be reaped locally. Replicated stores must either replicate
  retention with the blob or run the reaper only on the primary site's
  stores. This must be settled before the first cross-site deployment.

## Open questions

- Log compaction: keep N days, or compact into a periodic snapshot?
- Primary failover: manual promotion only (simplest), or a lease?
- Auth: replicas need a read credential for the log; is the existing
  read-access role sufficient, or does the log need its own scope?
- Do any clients need read-your-writes against a replica? If so, a write
  could return its sequence number, and the client could wait for the
  replica to reach it.
