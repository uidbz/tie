# tie — code review, risk map and simplification options

Review date: 2026-10-16 · Commit reviewed: `4e8f8ac` (master) · ~33k lines of Go.

This is a whole-repository review: correctness, security, durability, concurrency,
performance, API/package design, Unix-philosophy fit, and options for refactoring
and simplification. Every finding names a file and line, a concrete failure
scenario, and a recommended fix. Where several fixes are possible the recommended
one is marked **(recommended)**.

## How to read this

- **Severity**: `CRITICAL` (exploitable / data loss / wedges the server), `HIGH`
  (silently wrong results or likely data damage), `MEDIUM` (real bug in a narrower
  scenario, or a serious latent risk), `LOW` (cleanup, polish, minor inefficiency).
- **Evidence**:
  - **[reproduced]** — confirmed with a throwaway program or test during the review
    (reproducers are in the [appendix](#appendix-a--reproducers); none were
    committed).
  - **[code-read]** — follows directly from reading the code; not executed.
- `go build ./...` and `go vet ./...` are clean. All non-client test packages pass,
  including under `-race` (`tiedb`, `io/tiefs`, `webservice`, `cmd/tie-filehost`).
  The `client` package was **not** run: its suite writes into whatever stack is
  listening on `:2161`, and one was running at review time. The race-clean result
  means the existing tests don't reach the races described below; it isn't evidence
  that those races are absent.
- `staticcheck` in `$GOPATH/bin` is too old for Go 1.27 export data and could not
  run; see [§10](#10-tooling-and-process).

---

## 1. Executive summary

The engine has clearly been hardened through hard-won debugging (the `putAssoc`
lost-update fix, single-owner `db_size`, writer-owns-fd, read-your-writes in
`readSlot`), and the code is unusually well commented. The biggest remaining
risks are in four places: **the filehost's request handling**, **lock discipline
in tiedb's iteration paths**, **silent result truncation at the query layer**, and
**the shared blob cache used by FUSE/SMB**.

### Top 12, in priority order

| # | Sev | Finding | Where |
|---|-----|---------|-------|
| 1 | CRITICAL | Filehost path traversal → **arbitrary file read** (and existence/size probe) for anyone with the read role, which is anonymous by default **[reproduced]** | `cmd/tie-filehost/main.go:258`, `:329`, `:354` |
| 2 | CRITICAL | `ForEachTriple` (i.e. `tie dump`) + any concurrent `Add` of a new subject **deadlocks the collection permanently** **[reproduced]** | `tiedb/collection.go:1104-1105` |
| 3 | CRITICAL | Filehost writes uploads in place at the final content address; a crash, a kill or two concurrent uploads of the same blob leave a **corrupt blob that dedup then keeps forever** | `cmd/tie-filehost/main.go:197-227` |
| 4 | HIGH | Queries with `Limit: 0` are capped at **1000 triples server-side**, but many callers treat 0 as "no limit": large directories, tag views, tag lists and version lists **truncate silently**, and reconciliation and `RenameDir` act on partial data | `tiedb/collection.go:808`; `client/tag.go:664,1033,1957`; `client/versions.go:107,211`; `io/tiefs/tree.go:166,175` |
| 5 | HIGH | FUSE/SMB blob cache **closes the fd of an evicted blob that readers still hold** → EIO mid-read, and repeated re-downloads ("thrash") of large files **[reproduced]** | `io/tiefs/cache.go:103-125` |
| 6 | HIGH | Filehost reports **success for a checksum-mismatched upload** (deletes the file, then returns the hash and records retention) | `cmd/tie-filehost/main.go:221-255` |
| 7 | HIGH | `Sort` allocates **~533 KB per call** (a 10 000-slot channel plus a 1 000-slot slice), even for a one-triple set; Expand/Dump/CoTags call it once per key **[reproduced]** | `tiedb/collection.go:811-814` |
| 8 | HIGH | `name` triple corrupted by `strings.TrimRight(filename, ext)` (a cutset, not a suffix): `map.mp3`→`ma`, `bigpig.jpg`→`bigpi` **[reproduced]** | `client/tag.go:786` |
| 9 | HIGH | Triplestore namespace/collection come straight from the request: path traversal for `.tie` files and directories, and any read-role caller can **create unbounded collections** (~10 MB of channel buffers plus a goroutine, a file and a directory each) | `webservice/webservice.go:164-178`, `tiedb/tietree.go:127-163` |
| 10 | HIGH | The per-call `collection` parameter has **mixed semantics**: dirs and lookups go to the bound collection while batches go to the named one, and the name is used raw rather than resolved through `[Collections]`. `verify --collection X --repair` turns orphans into dangling refs | `client/tag.go:420`, `client/write.go:36`, `client/verify.go:430`, `client/tie.go:363` |
| 11 | MEDIUM | `CheckIndex` **nil-derefs on exactly the dangling-id case it exists to report** | `tiedb/collection.go:648`, `tiedb/checkindex.go:185,245` |
| 12 | MEDIUM | `Sync` is a process-global `WaitGroup` used concurrently with `Add` (a documented misuse), starves under steady load, doesn't fsync, and ignores deletes. Every client `Batch(); Sync()` pair is one redundant round-trip | `tiedb/collection.go:331`, `api/batch.go:110` |

The rest of the document expands on these and on ~60 further findings, followed by
a Unix-philosophy review ([§6](#6-unix-philosophy-review)), refactoring options
([§7](#7-refactoring-options)), simplifications ([§8](#8-simplification-options)), a
risk map ([§9](#9-risk-map-where-bugs-are-likely-to-live)) and a suggested order
of work ([§11](#11-suggested-roadmap)).

---

## 2. Critical findings

### 2.1 CRITICAL — Filehost path traversal: arbitrary file read  **[reproduced]**

`cmd/tie-filehost/main.go:258` (`DownloadHandler`), and the same pattern in
`StatHandler` (`:329`), `NamedDownloadHandler` (`:354`), `SetRetentionHandler`
(`:393`) and `GetRetentionHandler` (`:424`).

The handlers validate the `{hash}` wildcard only with `len(hash) != 64`. Go's
`ServeMux` matches on the *escaped* path but hands the wildcard to the handler
*unescaped*, so `%2F` turns into `/`. `PathFromHash` (`:149`) then
`filepath.Join`s the value under the store root.

```
GET /..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2F..%2Fetc%2Fpasswd
→ PathValue = "../../…/etc/passwd" (exactly 64 chars) → PathFromHash = "/etc/passwd"
```

Verified with a standalone `ServeMux` probe ([A.1](#a1-servemux-unescapes-2f-inside-a-wildcard)).
`findBlob` stats the path, so `GET` serves the file, and `HEAD` acts as an
existence-and-size oracle for any path. The read routes require
`auth.AccessRead`, and the **default `AnonymousAccess` is `"write"`**, so on a
default install this is unauthenticated. Obvious targets: `tie-filehost.toml`
(plaintext passwords), the triplestore's `db/*.tie` if it runs as the same user,
SSH keys.

`UploadHandler` has the same unvalidated `h` (`:158`, `:182`). With traversal it
can `MkdirAll` arbitrary directories, and it briefly creates files outside the
store (removed again on the checksum mismatch). The retention handlers write
junk keys into the index.

**Fix (recommended):** validate with `metadata.IsHexHash` once, centrally. Either
wrap every `{hash}` route in a `requireHash` middleware, or make `PathFromHash`
return `(string, error)` and refuse non-hex input. Defence in depth: after
joining, check that `filepath.Rel(root, p)` doesn't start with `..`. Add a
regression test that sends `%2F` sequences to every route.

### 2.2 CRITICAL — `ForEachTriple` deadlocks against concurrent writes  **[reproduced]**

`tiedb/collection.go:1102-1127`.

```go
emit := func(level int, tree *lockedTree[uint64, *AssociationSet]) {
    tree.ForEach(func(entryID uint64, _ *AssociationSet) {   // holds shard RLock
        subtree := ic.getAssociations(level, entryID)         // RLocks the SAME shard again
```

`lockedTree.ForEach` (`lockedtree.go:115-124`) holds a shard's `RLock` while it
runs the callback, and the callback looks the same key up again in the same tree.
That's a recursive read lock. `sync.RWMutex` forbids it: once a writer is queued
in `Lock`, new `RLock`s block. `Add` → `insertAssociation` → `putAssoc` →
`GetOrPut` takes that `Lock` for any new subject in the shard **while holding
`changeMutex`**. The result is a permanent deadlock. The dump goroutine waits on
the writer, the writer waits on the dump's first `RLock`, and every other writer
in the collection waits on `changeMutex`.

The goroutine dump in [A.2](#a2-foreachtriple-deadlock) shows exactly this:
`lockedtree.go:120 ForEach → collection.go:1105 getAssociations →
lockedtree.go:51 RLock` versus `collection.go:165 Add → lockedtree.go:82 Lock`.
The writer froze after 13 adds.

In production: running `tie dump` (or the cron backup in `contrib/backup`) while
anything imports wedges the collection until restart. Because `Dump` streams to
the network inside the callback, a slow client also holds those shard locks for
as long as the transfer takes.

**Fix (recommended), two parts:**
1. Use the set `ForEach` already passes in instead of re-fetching it. That's a
   one-line change that removes the recursion.
2. Don't call out to arbitrary code, or to the network, while holding a shard
   lock. Snapshot each shard's `(key, *AssociationSet)` pairs under the lock,
   release it, then process the snapshot. Pointers to sets are stable, so a
   snapshot costs one slice per shard. Do the same in `CheckIndex`, which nests
   `ForEach` → `levelOf` (safe today only because it holds `changeMutex`).

Add a regression test like the reproducer, and a comment on `lockedTree.ForEach`:
"fn must not touch this tree".

### 2.3 CRITICAL — Filehost upload is not atomic; corrupt blobs become permanent

`cmd/tie-filehost/main.go:197-227`.

For `PUT /upload/{hash}` (the path `putlib` always uses) the handler:
1. `os.Stat(dest)` → if it exists, records retention and returns `h`
   **without verifying the stored bytes** (`:197-203`);
2. otherwise opens `dest` **at the final content-addressed path** with
   `O_CREATE|O_APPEND` (`:205`) and streams the body into it;
3. hashes the file afterwards, and removes it on mismatch.

Failure scenarios:
- **Crash, `kill -9`, power loss or disk full mid-upload.** A truncated file sits
  at the content address. Every later upload of the same content hits step 1,
  and the corruption is never repaired. Downloads serve it, `verify --check-blobs`
  (HEAD only) reports it present, and only `mount --verify` would notice.
- **Two concurrent uploads of the same hash** (two clients, or `import` mirroring,
  or a retry after a timeout while the first request is still streaming). Both
  pass the `Stat`, both `O_APPEND` into the same file, and the content
  interleaves. Each request hashes the doubled file, sees a mismatch and removes
  it. Whether data is lost then depends on timing: one request may hash before
  the other finishes appending and report success, and then the other deletes the
  file.
- **No `fsync`** before acknowledging, so after a power cut a hash can be
  acknowledged with no bytes behind it.

**Fix (recommended):** write to `os.CreateTemp(filepath.Dir(dest), ".upload-*")`
in the *same directory*, so the rename can't fail with `EXDEV`. Hash while
copying with `io.MultiWriter`, so there's no second read pass. `f.Sync()`, then
either `os.Link(tmp, dest)` (which fails with `EEXIST` if a concurrent upload won,
and that's fine because the content is identical) or `os.Rename`, then remove the
temp file. Optionally `fsync` the directory. This also makes the hashless upload
path (`/upload`) share one implementation, which fixes §3.4.

---

## 3. High-severity findings

### 3.1 HIGH — Silent 1000-triple cap on "unlimited" queries  **[code-read]**

`tiedb/collection.go:808-810`:

```go
if o.Limit == 0 {
    o.Limit = 1000
}
```

`QuerySpec.Limit` passes straight through to it (`client/tie.go:453`). Only some
callers know that and pass `-1`. These pass `0`, explicitly or by omission:

| Caller | Consequence when there are more than 1000 matches |
|--------|------------------------------------|
| `ReadTieDir` (`client/tag.go:1033`) | Directory listings in FUSE, SMB and `stat` silently miss children. `DeleteDir`'s emptiness check still works, but `collectDescendantDirs` (`:1206`) misses subdirectories, so **`RenameDir` leaves stale `path` triples on the missed descendants** and they become duplicate or dangling paths. |
| `fileChildren` (`client/tag.go:664`) | `reconcileDir` and `WriteFile` see only the first 1000 children. Superseded files beyond that are never versioned, and `WriteFile` may miss an existing same-named file and create a duplicate. |
| `ListDirTypes` (`:1957`) | Truncated registry. |
| `listVersionRecords` / `ListVersions` (`client/versions.go:107,211`) | Retention trimming and listing see a subset. |
| `tiefs` tag views: `FilesWithTags(…, 0, 0)` and `ListTags(0, 0)` (`io/tiefs/tree.go:166,175`) | `query/<tag>` directories and the tag list cap at 1000. The docs on `ListTags` and `FilesWithTags` say "limit <= 0 means no limit", which is wrong for 0. |
| `tie get` (CLI default `--limit 1000`) | Visible to the user, so acceptable. |
| `Associated` request (`api/associated.go`, `SortOptions{}`) | Both the reverse set and each forward set cap at 1000. |

Note also that `Limit` counts **triples**, not rows. `QueryReply.TotalCount`'s
comment says "number of matching keys", which is only true when each key
contributes exactly one triple, e.g. with a `Filter`.

**Fix options:**
- (a) **(recommended)** Normalize on the client: in `QueryIn`, map `Limit <= 0`
  to `-1` and document "0 = unlimited" everywhere. On the server, keep a large
  default only for requests that genuinely omit it, or make it unlimited and add
  a server-side `MaxRows` config knob for load-shedding.
- (b) Make `SortOptions.Limit` a `*int` / `Unlimited` sentinel. More honest, but a
  wire-format change.

Either way, add a test that creates 1500 children and asserts that `ReadTieDir`
returns all 1500.

### 3.2 HIGH — FUSE/SMB cache closes fds that live readers hold  **[reproduced]**

`io/tiefs/cache.go:102-125`. `evictLocked` calls `e.file.Close()` on the evicted
entry, but a `Blob` is just `{entry *cacheEntry}` and reads through `entry.file`.
The comment ("any reader still holding a reference to that entry keeps reading
through its own fd") and CLAUDE.md ("an evicted entry stays readable through fds
that already resolved it") describe an invariant the code doesn't implement:
readers don't have their own fd.

- **SMB** (`io/smbfs/handle.go:117`) caches the `Blob` on the handle. After
  eviction every read fails with `file already closed`. The reproducer in
  [A.3](#a3-cache-eviction-closes-a-live-blob) gets exactly that error.
- **FUSE** (`io/fuselib/fuselib.go:181`) re-resolves the `Blob` on every `Read`,
  so it mostly re-downloads instead. That has two consequences:
  (i) between `Blob()` returning and `ReadAt` running, another goroutine can
  evict and close the fd, giving an `EIO`;
  (ii) with the default 1 GB budget, two large files read alternately (two
  players, or one player plus a thumbnailer) evict each other on every read, and
  each eviction costs a **full re-download of a multi-GB blob**.
- The "LRU" is insertion order (FIFO): a hit doesn't move the entry, so the
  hottest file is evicted as readily as the coldest.

**Fix (recommended):** reference-count entries. `Blob()` increments `refs` and
`Blob.Close()` decrements it (callers already have Release/Close hooks).
Eviction removes the entry from the map and unlinks the file, but closes the fd
only when `refs == 0`; otherwise the last `Close` does it. Move the entry to the
back of `order` on every hit (or use `container/list`) to get real LRU. Hold the
`Blob` per open file handle in FUSE as SMB already does, instead of per `Read`.

### 3.3 HIGH — Filehost reports success for a checksum-mismatched upload  **[code-read]**

`cmd/tie-filehost/main.go:221-255`. On `hashHex != h` the handler removes the
file (`:222-227`) but **doesn't return**. It falls through to
`recordUpload(hashHex, …)` and `fmt.Fprint(w, hashHex)` with a 200. `putlib`
then compares the returned hash with its local hash, sees a mismatch and reports
"Upload checksum failed". So the client fails correctly, but only by accident:
the server has (a) returned 200 for a failed upload, (b) recorded a retention
entry for a blob that doesn't exist, and (c) any other client (curl, Python) that
trusts a 200 believes the content is stored.

**Fix:** `http.Error(w, "checksum mismatch", http.StatusUnprocessableEntity); return`.
That falls out naturally from the §2.3 rewrite.

### 3.4 HIGH — Hashless upload breaks when `/tmp` is a different filesystem  **[code-read]**

`cmd/tie-filehost/main.go:173-179, 238-243`. `PUT /upload` (no hash) stages the
body in `os.MkdirTemp("", …)`, normally `/tmp`, often a tmpfs or a different
volume in Docker, and then calls `os.Rename` into the store. Across filesystems
that fails with `EXDEV`. The handler then writes
`"Error saving file on server:" + err` with **status 200**, and **`err` is the
stale `Stat` error** (`nil` here), not `errMove`. A client reading the body as a
hash receives garbage. Staging in RAM-backed `/tmp` also caps upload size at
free RAM. Same fix as §2.3: stage inside the store directory.

### 3.5 HIGH — `Sort` allocates ~533 KB per call  **[reproduced]**

`tiedb/collection.go:811-814`:

```go
c := make(chan Triple, 10000)          // 10 000 × 48 B = 480 KB, zeroed
go ic.loadTriples(tree, c)             // + a goroutine
sorted = make([]StringTriple, 0, 1000) // 1 000 × 48 B = 48 KB
```

Measured at **533 059 bytes allocated per single-key, single-triple
`ExpandKeys`** ([A.4](#a4-sort-allocation)). `Sort` runs once per key in
`ExpandKeys`, `ForEachTriple`, `CoTags` and `Get` (twice, forward and reverse),
so:
- a `Query{Expand: true}` returning 1000 rows allocates **≈ 0.5 GB**;
- `tie dump` of a collection with 300k subjects allocates **≈ 160 GB** of garbage
  (a single `ForEachTriple` over 20k subjects took ~2 s in memory mode);
- `firstValueOf` for `SortByValue` calls `Get` per matched key, so a sorted page
  over 10k keys costs 10–20 GB of churn.

This directly contradicts the project's memory-over-speed weighting, and it's
likely the single largest CPU cost in the triplestore.

**Fix (recommended):** `Sort` doesn't need a channel or a goroutine.
`tree.ForEach` can resolve and append directly into a slice pre-sized with
`tree.Size()`. If streaming is wanted later, use an unbuffered or `Size()`-bounded
channel. Benchmark before and after (`BenchmarkExpandKeys`, `BenchmarkDump`) as
CLAUDE.md asks.

### 3.6 HIGH — `name` triple corruption via `TrimRight`  **[reproduced]**

`client/tag.go:786`:
`name := strings.TrimRight(filename, filepath.Ext(filename))`. `TrimRight` treats
its second argument as a **set of characters**:

```
bigpig.jpg → "bigpi"   (should be "bigpig")
map.mp3    → "ma"      (should be "map")
```

Every imported file whose stem ends in a character that appears in its extension
gets a wrong `name`. `RenameFile` (`:1142`) already uses `TrimSuffix` correctly.
`verify --fix` rebuilds `filename` from `name + extension`, so the corruption can
propagate into `filename` during repair.

**Fix:** `strings.TrimSuffix`. Add a test, then a one-off `media backfill`-style
migration that recomputes `name` from `filename` for existing data.

### 3.7 HIGH — Triplestore trusts request-supplied namespace/collection as paths  **[code-read]**

`webservice/webservice.go:164-178` → `tiedb/tietree.go:68-79, 127-163`.

- `DbPath(namespace)` is `filepath.Join(DbPath, namespace)`, and the collection
  id is concatenated: `path + "/" + dbname + ".tie"`. A write-role caller can use
  `Namespace: "../../home/x"` to create or append `.tie` files anywhere the
  service user can write. `Drop` then deletes any `*.tie` file reachable that way.
- **Any read-role caller** (including anonymous, if
  `AnonymousAccess = "read"`) creates a brand-new collection by querying a new
  name. `GetCollection` runs `MkdirAll`, `O_CREATE`s the file and starts a writer
  goroutine with `make(chan FileMod, 100000)` plus `make(chan ReadRequest, 100000)`,
  about **10 MB of channel buffer per collection**, never freed. A loop of
  `Query` requests with random names exhausts memory, fds and inodes.
- `GetCollection` holds the **global** `colMutex` during `loadDB`. Lazily loading
  a big collection (e.g. the first touch of `Main_prev`) blocks **every request
  to every collection** for the whole load.

**Fix (recommended):**
1. Validate both names against `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` (no `..`, no
   separators) in one place, `Webservice.collectionKey`.
2. Distinguish read access from create: a read of a non-existent collection
   returns "not found" instead of creating it. Only write requests may create.
3. Load outside `colMutex`. Insert a placeholder with a `ready` channel (the
   single-flight pattern the blob caches already use), so concurrent requests
   for the loading collection wait while others proceed.
4. Size the writer queues proportionally (e.g. 4096). The buffer only needs to
   absorb bursts; back-pressure is fine.

### 3.8 HIGH — The `collection string` parameter means two different things  **[code-read]**

`TieClient` is bound to one resolved collection (`tc.active`), and many methods
also take a `collection string` that only some of their internal calls honour:

- `ImportDir(…, collection, …)` (`client/tag.go:420`) writes tag batches to
  `collection`, but `MkTieDirAll` → `DirUIDFromPath` → `Query` and `reconcileDir`
  → `fileChildren` → `Query` use the **bound** collection. So `tie import
  --collection X` creates directory nodes in the default collection and file
  triples in `X`, with parent edges pointing at DirUIDs that don't exist in `X`.
- `WriteFileWithProgress` (`client/write.go:36`) does the same: children and old
  metadata are read from the bound collection, the batch goes to `collection`,
  and `Sync` targets the bound one.
- `RepairOrphans` (`client/verify.go:430`) creates the restore dir with
  `MkTieDirAll` in the **bound** collection and re-parents the orphans of
  `collection` to it. So `tie verify --collection X --repair` converts every
  orphan into a dangling parent reference, and the post-repair re-verify then
  reports them as new problems. `repairSession.run` (`client/repair.go`) has the
  same issue for `DEST`.
- `collectionInfo(name)` (`client/tie.go:363`) uses the name **verbatim as the
  collection id** in the bound namespace, whereas `ResolveHosts(name)` resolves
  it through `[Collections.<name>]`. For an entry
  `[Collections.music] Collection = "Music2"`, `tie import --collection music`
  takes its hosts from the `music` entry but writes to collection id `music`.

**Fix (recommended):** remove the parameter from every method, and add
`func (tc *TieClient) In(name string) *TieClient` that returns a client bound to
`Config.ResolveCollection(name)` (cheap: it shares the `ws.Client` when the URL
and credentials match). Callers write `tie.In(x).ImportDir(...)`. This deletes
the `*In` method family (`QueryIn`, `ExpandIn`, `GetIn`, `SyncIn`, `NewBatchIn`,
`ensureAddedAtIn`, `collectionMembersIn`, …) and makes this whole class of bug
impossible. See [§7.2](#72-client-collection-scoped-handles-recommended).

---

## 4. Medium-severity findings

### 4.1 `CheckIndex` panics on dangling ids  **[code-read]**
`tiedb/collection.go:648`: `getValue(0, id)` does
`ic.getUniqueValue(0, id).Value[:]` with no nil check. `CheckIndex` calls
`sample(...)` → `getValueString(level, id)` for **dangling** ids
(`checkindex.go:185`, where `levelOf` failed and the level defaults to 0) and
for reverse-only subjects whose level is unknown (`:245`). The very
inconsistency the check exists to report crashes the handler. `net/http`
recovers it, `changeMutex` is released by `defer`, and the client sees a dropped
connection instead of a report. **Fix:** `getValue` returns `nil` on a missing
entry at any level, and `sample` renders unknown ids as `#<id>`.

### 4.2 `Sync` semantics are wrong in four ways  **[code-read]**
`tiedb/collection.go:331` (`finishedAdding.Wait()`), `:168` (`Add(1)`),
`filehandling.go:297` (`Done()`), `checkindex.go:298`.
1. **WaitGroup misuse.** `Add` with a positive delta from a zero counter, running
   concurrently with `Wait`, is explicitly disallowed by the `sync` docs and can
   panic ("WaitGroup is reused before previous Wait has returned").
2. **Starvation.** It waits for *every* client's writes. Under a steady import
   from client B, a `Sync` from client A may never return.
3. **Not durable.** It waits until bytes reach the page cache (`WriteAt`), not
   `fsync`, so "Sync" doesn't mean what callers assume.
4. **Deletes aren't counted.** `FILE_DELETE` never touches the WaitGroup.

Meanwhile reads are already read-your-writes: the in-memory index is updated
synchronously, and `readSlot` drains the queue. So **the client-side `Sync`
calls buy nothing for correctness**. `Batch` already calls `col.Sync()`
server-side (`api/batch.go:110`), which makes every `tie.Batch(b); tie.Sync()`
pair (≈25 call sites in `client/`) a redundant round-trip.

**Fix (recommended):** replace the WaitGroup with a *barrier* message.
`Sync(ctx)` enqueues `FileMod{Mode: FILE_BARRIER, Done: ch}` and waits for the
writer to close `ch` once it has applied everything queued before it, then
`fsync`s. That's FIFO-precise, per-caller and durable. Then drop the client
`Sync` calls after `Batch`, and keep `Sync` as an explicit durability request.

### 4.3 Reused slot can be read before its write is queued  **[code-read]**
`Add` inserts into the index (`collection.go:165`) *before* enqueueing the
`FILE_ADD` (`:169`). `readSlot`'s retry loop (`filehandling.go:344-372`) covers
a fresh append slot (`pos >= file size` → `ReadAt` returns EOF). It doesn't cover
a **reclaimed** slot: `ReadAt` succeeds and returns the old tombstone,
`bufToAssociation` fails, and the triple is silently dropped from that query.
The window is small (both steps run under `changeMutex`), but it's the same bug
class the retry loop was written to kill ("dropping the triple … led
reconciliation to delete live directories").

**Fix (recommended), which also simplifies:** enqueue first, then insert into
the index. Both happen under `changeMutex`, and a channel send happens-before
the matching receive, so any reader that can see the index entry finds the write
already in the queue and `drainWriteQueue` applies it. The 100 000-iteration
sleep loop in `readSlot` can then go.

### 4.4 Data race in `AssociationSet.ForEach` (small tier)  **[code-read]**
`tiedb/associationset.go:248-258` copies the `s.small` slice header, releases
`changeLock`, and iterates the **shared backing array**. A concurrent `Put`
updating a position in place (`:160`) or a `Delete` swap-removing (`:209-213`)
writes that array without the reader's lock. That's a data race (it will trip
`-race` under the right test). It also makes a concurrent query visit the moved
element twice (a duplicate triple) or see a deleted one. **Fix:** copy the slice
under the read lock (≤256 entries × 24 B, which is cheap), or iterate under the
lock as the sharded tier does.

### 4.5 `DropCollection` races in-flight requests  **[code-read]**
`tiedb/tietree.go:105-125` closes the writer and deletes the collection from the
map, but a request that already holds the `*Collection` (from an earlier
`GetCollection`) keeps using it:
- its writes go into `dBWriteQueue` with no reader, so they're buffered then
  lost, or block forever once 100 000 are queued;
- its disk reads (`readTripleAt`) send to `dBReadQueue`, which no one serves:
  **the goroutine hangs forever**, holding its `MaxConcurrentRequests` slot.

`restore --drop` during any other activity triggers it. **Fix:** give
`Collection` a `closed` state checked under a `sync.RWMutex` (requests hold
`RLock` for their duration and `Drop` takes `Lock`), or reference-count
collections the way §3.2 does for blobs.

### 4.6 The writer `panic`s on I/O errors; library code calls `os.Exit`  **[code-read]**
`tiedb/filehandling.go:310-315` panics on any write error, including `ENOSPC`.
The panic is on the writer goroutine, so it **kills the whole triplestore** and
every collection, and any acknowledged writes still in the 100 000-entry queue
are lost. `tietree.go:145,151,157` call `os.Exit(1)` from inside the library
(`GetCollection` during a request), and `webservice.go:117-130` exits from a
library function too. **Fix:** put the collection into a sticky read-only/error
state (writes return an error, reads keep working), surface it through `Add` and
`Batch` return values and log loudly. Return errors from
`initialize`/`GetCollection`. Exit only in `main`.

Related: the durability contract is "acknowledged once in the in-memory queue"
(up to 10 s plus 100 000 mods of loss on a crash). That's a legitimate choice,
but it should be stated in the README. Pair it with the barrier `Sync` from §4.2
for callers that need more.

### 4.7 Reaper TOCTOU: it can delete a blob that was just re-extended  **[code-read]**
`cmd/tie-filehost/reaper.go:53-90`. `expiredSet` snapshots expired hashes;
marking then walks every blob (seconds to minutes on a big store); the sweep
(`:74`) deletes from the snapshot **without re-checking** the entry under the
lock. Two ways this loses data:
1. a client re-uploads the same bytes with `infinite` retention mid-pass;
   `recordUpload` extends the entry, then the reaper deletes the blob and the
   now-permanent entry;
2. a directory blob uploaded mid-pass that references an expired child isn't in
   the `listBlobs` snapshot, so the child is reaped.

**Fix:** under `s.retention.mu`, re-check
`e, ok := entries[h]; ok && e.ExpiresAt != 0 && now >= e.ExpiresAt` before
removing. For (2), treat blobs with an mtime newer than the pass start as roots,
or take a store-level `RWMutex` that uploads hold in read mode for the
`Stat`-to-record window.

### 4.8 Filenames with TAB/LF corrupt tiedir manifests  **[code-read]**
`io/putlib/putlib.go:263-268` writes `x.Name()` raw into a TAB/LF-delimited
line. Linux allows both characters in filenames. `ParseDirLine` then rejects the
line or splits it wrongly, and its callers `continue`:
- download and mount silently omit the entry;
- **the reaper's `dirChildren` skips it, so the child isn't protected** and can
  be reaped while still referenced.

**Fix:** reject such names at upload with a clear error, or percent-encode `%`,
TAB and LF in the filename column (a `tiedir-v3` header keeps old manifests
parseable).

### 4.9 FUSE/SMB staged writes are deleted when the commit fails  **[code-read]**
`io/tiefs/write.go:185` `Close()` runs `commitLocked()` and then **always**
removes the temp file. If the filehost or triplestore is unreachable, the user's
just-written bytes are gone. Most applications ignore `close()`/`release()`
errors, so this is silent data loss. **Fix:** on commit failure, move the staging
file to a recoverable spool (e.g. `$XDG_STATE_HOME/tie/unsaved/<name>-<ts>`) and
log the path, or keep the handle in a retry queue.

### 4.10 `MkTieDir` is check-then-create, with string-matched errors  **[code-read]**
`client/tag.go:1806-1857`. Two concurrent creators (two imports, a FUSE `mkdir`
plus an import, or SMB) both see "no UID for path" and mint two DirUIDs, which
is the origin of the duplicate-path problem the dedup script and `verify` deal
with. Also:
- `MkTieDirAll` calls `CreateTieRootDir` (one query) and then `DirUIDFromPath`
  1–2 times **per ancestor on every call**, so deep imports cost O(depth) queries
  per directory even when everything exists;
- control flow depends on `strings.HasPrefix(err.Error(), "Root dir already exists")`
  and `HasSuffix(…, "Directory exists")`;
- `filepath.Dir` is used on virtual `tie:/…` paths (`:1836`, also
  `versions.go:resolveFileLoc`), which is wrong on Windows; use `path`.

**Fix (recommended):** a server-side `MkDir` request (or a generic
`AddIfAbsent(key=path, relation)` compare-and-set) executed under
`changeMutex`, so path→UID creation is atomic. Client side: sentinel errors
(`ErrDirExists`), `path` instead of `filepath`, and an ancestor cache inside
`MkTieDirAll`.

### 4.11 Transport: retries, timeouts, nil client  **[code-read]**
`webservice/client.go`.
- It retries **every** request on transport errors (`:51`), on the grounds that
  "write ops are idempotent". `Update` isn't: if the first attempt applied but
  the response was lost, the retry fails with "Did not find …", and the client
  reports an error for a successful write. `Batch` containing `update` has the
  same problem.
- **No timeouts.** `http.Client{}` without a `Timeout` and no `context`, so a
  stalled server hangs the CLI, the FUSE mount (every `ReadTieDir`) and tie-smb
  forever.
- `NewClient` returns `nil` for a URL shorter than 5 characters (`:150`).
  `NewTieClientFor` stores it, and the first request panics.
- 403 isn't special-cased: the plaintext body "Forbidden" fails `json.Unmarshal`
  and surfaces as `invalid character 'F'…`.

**Fix:** pass `context.Context` through `Run`; set a default timeout (e.g. 60 s,
configurable); retry only requests marked idempotent; return an error from
`NewClient`; map any non-2xx status to a typed error.

### 4.12 `Batch` is partially applied, and a nil `Batch` panics  **[code-read]**
`api/batch.go:76`. A failing op returns after earlier ops were applied, with no
rollback. That's fine if documented, but the comment ("every op is applied and
checked") reads like atomicity. `{"batch": null}` dereferences `request.Batch`
and panics. **Fix:** a nil check, and document "non-atomic, stops at first
failure, ops before it remain applied". Longer term, see §7.4 for atomic
batches under `changeMutex`.

### 4.13 Read-modify-write ops aren't atomic  **[code-read]**
`Update` (Delete then Add), `SetValues` and `SimpleUpdate` (Get, Delete each,
Add) take and release `changeMutex` per step. A concurrent reader can observe
the relation empty, and two concurrent `Set`s on the same key/relation can end
with the **union** of both value sets, breaking "Set makes it hold exactly
these". **Fix:** run each of these under one `changeMutex` hold, using unlocked
internal `addLocked`/`deleteLocked` helpers.

### 4.14 `Collection.Get` merges reverse triples into forward results  **[code-read]**
`tiedb/collection.go:179-196`. `Get(key, rel)` returns forward triples **and**
reverse triples whose relation is `rel`, keyed by the other subject. For a
self-referencing triple (`uid parent uid`, which is exactly the root directory),
`data[key] = associated[key]` **overwrites** the forward map for `key`. `Get` is
the primitive under `SetValues` and `SimpleUpdate`, so "replace parent on root"
could delete the wrong set. It also resolves the whole reverse set. **Fix:** make
`Get` forward-only (its callers only read `data[key]`), and add an explicit
`GetReverse` if anything needs the other direction.

### 4.15 Webservice request handling  **[code-read]**
`webservice/webservice.go`.
- `io.ReadAll(r.Body)` (`:187`) with no limit: one oversized request exhausts
  memory. Use `http.MaxBytesReader` (configurable; `Restore` batches are large).
- An unknown request id (`:216`) logs and returns **200 with an empty body**;
  the client then fails JSON decoding. Return 404.
- Application errors are returned as **HTTP 200** with `Success:false`
  (`AnswerRequest`), which makes curl, proxies and monitoring blind. Use
  4xx/5xx and keep the JSON body.
- `ErrorToJsonString`'s fallback (`:227`) emits invalid JSON
  (`["Success": false…]`).

### 4.16 Filehost read cache  **[code-read]**
`cmd/tie-filehost/cache.go`.
- **Not restart-aware.** The `CachePath` directory is reused across restarts,
  but `entries` starts empty, so files from previous runs are never counted or
  evicted. The cache grows without bound across restarts. Either scan the
  directory at start-up or wipe it.
- **Copy-before-serve.** The first `GET` copies the *whole* blob from slow
  storage to the cache before sending the first byte. For a multi-GB video
  that's seconds to minutes to first byte, and HTTP clients may time out. Serve
  from the source and populate the cache asynchronously, or tee.
- FIFO rather than LRU (same as §3.2), and it's a near-copy of
  `io/tiefs/cache.go` (see §8.4).

### 4.17 Unbounded memory in long-lived mounts  **[code-read]**
`io/tiefs/tree.go:78-90`. `Store.hashToInode` grows by one entry per distinct
key ever listed (hashes, archive members, query paths) and is never pruned.
For a tie-smb server that runs for months over a large library, that's a slow
leak. **Fix:** derive inode numbers from a hash of the key (64-bit FNV or
xxhash; collisions are tolerable for FUSE/SMB if the inode is only a cache key),
which removes the map entirely.

### 4.18 `restore` holds the whole dump in memory and sends one request  **[code-read]**
`cmd/tie/commands.go:363-390`, `client/tie.go:640`. The whole TSV is parsed into
`[][3]string` and sent as **one** `Batch` JSON body. A 2M-triple dump is
hundreds of MB on both sides, it sits under one semaphore slot, and if it fails
nothing is retried. **Fix:** stream and send chunks (e.g. 5 000 ops per batch),
with a progress count on stderr. Validation-before-drop can stay a first pass
over the file, or use a temp copy for stdin.

---

## 5. Low-severity findings and performance

### 5.1 Correctness / robustness (low)

| # | Where | Finding | Fix |
|---|-------|---------|-----|
| L1 | `api/add.go`, `api/update.go`, `api/delete.go` | `reply.OrigValue1 = request.Value2`: a copy-paste bug, so every reply echoes value2 as value1. | `= request.Value1`. |
| L2 | `tiedb/collection.go:666` | `bytes.Trim(value, "\x00")` strips **leading** NULs too, and any value ending in NUL loses it. A value is not binary-safe. | `bytes.TrimRight`, and document "values must not end in NUL" (or store a length). |
| L3 | `tiedb/collection.go:416,451` | Padding always adds a full 24-byte zero block when `len%24 == 0`, so exact multiples cost one extra trie level and record. A format quirk; don't change without a migration, but document it. | Comment only. |
| L4 | `tiedb/collection.go:474`, `filehandling.go:102`, `collection.go:92` | `totalEntries` is read non-atomically (`GetTotalEntries`), written under `totalEntriesMutex` at load, and incremented atomically in `nextID`. Three synchronization schemes for one counter. | `atomic.Uint64` everywhere; drop the mutex (use a CAS max-loop at load). |
| L5 | `tiedb/filehandling.go:262-275` | The comment describes a `Position == -1` placeholder that no longer exists (slots are pre-allocated). | Delete the stale branch and comment. |
| L6 | `tiedb/datastructures.go:41-45` | `FileIndexer` interface is unused. `// ASSOCIATED` constant is commented out. | Delete. |
| L7 | `cmd/tie-triplestore/main.go:19-22` | Unused globals `db`, `dbPath`. | Delete. |
| L8 | `cmd/tie/commands.go:43-44` | `tie add` declares `--gallery-name` and `--tags` and ignores them. | Delete. |
| L9 | `cmd/tie/commands.go:746,770` | `tie del`: a query error is printed to **stdout** and the command exits 0; the `* <value>` case prints help to stdout and exits 0. | Return errors (exit ≠ 0) and write to stderr. |
| L10 | `client/tie.go:405-418` | `SyncIn` ignores `reply.Success`. | `replyError`. |
| L11 | `client/misc.go:150-168` | `GetVideoHeight` shells out to `ffprobe` and prints errors to stdout; nothing in-tree calls it. `ReadConfig` is deprecated and swallows errors. | Delete or move to the caller that needs it. |
| L12 | `client/misc.go:142` + `client/tie.go:34` + `cmd/tie-filehost/main.go:111` + `metadata/tiedir.go:26` | The content-address key constant exists **four** times; `Config.key` is unused. A divergence would silently fork the address space. | One `metadata.NewHash()`; delete the rest. The filehost's `AddressOf`/`InitKey` become `metadata.HashReader`. |
| L13 | `io/tiefs/*.go` | Directory `tiefs`, package `tiesfs`. Every importer has to alias it mentally; grep finds neither consistently. | Rename the package `tiefs`. |
| L14 | `io/putlib/putlib.go:210-230` | `PathToWorkdir` calls `os.Chdir`, which is process-global and unsafe once the library runs inside FUSE/SMB. It's unused. | Delete the option. |
| L15 | `io/putlib/putlib.go:238` | `Lstat` plus `os.Open`: a symlink to a directory is treated as a file, and hashing fails with "is a directory", which **aborts the whole tree import**. | Follow the link explicitly (`Stat`), or skip symlinks with a warning, or apply a policy flag. |
| L16 | `client/repair.go:run` | The journal line is written and `Applied` incremented **before** the batch is sent, so on a failed batch the journal claims mutations that never happened. | Journal after a successful `Batch`, per chunk. |
| L17 | `client/tie.go:21-31` | `defaultConfig` carries `defaultuser`/`defaultpassword`, and `LoadOrCreateConfig` writes them to disk. | Write empty credentials and a comment. |
| L18 | `auth/auth.go` | A configured user with an empty `Password` authenticates with any username-match and an empty password. Probably intended, but undocumented. Duplicate usernames silently override. | Reject both at config load. |
| L19 | `webservice/client.go` / `client.HTTPClientFor` | A new `http.Transport` per call for insecure or authenticated hosts, so connections are never pooled across `HTTPClientFor` calls (verify, repair and stat each build their own). | Cache one client per `FileHost` on `TieClient`. |
| L20 | `tiedb/datastructures.go:308-377` | `EntryToBytes`/`HashToBytes`/`toBytes` allocate 6–8 tiny slices plus a discarded `make([]byte, ENTRY_SIZE)` per record. | Write into one `[ENTRY_SIZE]byte` with `binary.LittleEndian.PutUint*` at offsets (mirrors `bufTo*`). |
| L21 | `metadata/tiedir.go` | The `metadata` package imports `tiedb` only to construct a `BlobPolicy`. That layering is backwards: a format package depends on the storage engine. | Move `HexHashBlobPolicy` to `webservice` (its only server-side user) or into `tiedb` itself. |

### 5.2 Performance

Ordered by expected impact. Per CLAUDE.md, benchmark old vs new under the same
conditions before keeping any of these.

1. **`Sort`'s 533 KB per call** (§3.5). Largest win, trivial change.
2. **Re-upload of existing blobs.** `putlib` never asks whether the blob exists.
   It hashes the file (read #1) and then streams it all again (read #2), even
   though the server answers "exists" without reading the body (Go then has to
   discard or close the connection). Re-importing a library re-sends every byte.
   **Fix:** `HEAD /{hash}` first and skip the PUT on 200. On a network mount that
   cuts re-import I/O by about half; on a WAN, by nearly all of it.
3. **Each imported file is opened 4–5 times:** the album planner probes, then
   `GetTieTypeFromPath` sniffs, `ExtractMediaMetadata` sniffs again and parses
   tags, `AddressOfFile` hashes, and `UploadFile` streams. On the network mounts
   CLAUDE.md calls out, opens and first-block reads dominate. **Fix:** one
   `ingest(path)` that opens once, reads the head for sniffing, parses tags from
   the same `*os.File` (seekable), hashes, and returns everything; then the
   upload (or no upload, per item 2).
4. **Sort-then-paginate.** Every page of a query resolves (a disk read per
   uncached triple) and string-sorts **all** matches. A FUSE `ls` of a 100k-item
   tag directory, paged by 1000, is O(pages × N log N) disk reads. Options:
   (a) when `SortBy` is empty and pagination is used, sort by entry id (no
   resolution needed) and resolve only the page; (b) cache the sorted key list
   per (query, generation counter) for a few seconds.
5. **Filter after resolve.** `Sort` resolves every triple and then drops those
   whose `Value1` string differs from `value1Filter`. The relation id is already
   in the `UniqueAssociation` key, so resolve the filter string to an id once
   and skip non-matching entries **before** the disk read. For `Get(hub, rel)`
   on a high-fan-in node, this avoids reading the entire reverse set.
6. **`SortByValue` does a `Get` per key**, which with item 5 unfixed resolves
   both directions. Use a forward-only lookup restricted to the relation id.
7. **Sequential `--check-blobs`.** One `HEAD` at a time (`client/verify.go:328`).
   A worker pool of 16 makes a 1M-file check go from hours to minutes.
8. **`CoTags` resolves the whole matched set twice** (once to keys, once per key
   with a filter). With item 5 it reads only tag triples.
9. **Reaper** (`reaper.go`): every pass enumerates and **opens every blob** in
   every store to look for manifests. Index directory blobs at upload time (the
   filehost knows from the `tiedir-v` header) in a small per-store set file, and
   walk only those.
10. **`getValue` recursion** (`collection.go:643-655`) re-appends at every level:
    O(L²) bytes for an L-level value. Build into one buffer of `L*24` bytes,
    walking back to front.
11. **Load-time GC** (`filehandling.go:156`): `SetGCPercent(200)` is reasonable.
    Consider `debug.SetMemoryLimit` from config instead; it bounds the peak that
    a higher GOGC risks on small machines.

---

## 6. Unix-philosophy review

The project already gets several things right:
- **separate single-purpose daemons** (triplestore, filehost, tie-smb);
- **a filehost that stays a dumb byte store**;
- **TSV dump/restore** through stdin and stdout;
- **TTY-aware table vs TSV output** in `printRows`;
- **diagnostics to stderr** in tiedb;
- **a non-zero exit from `verify`** for cron.

The items below are where it drifts, roughly by impact.

### 6.1 Libraries write to stdout ("rule of silence", "stdout is data")
`client` writes progress chatter straight to **stdout**:
- `ImportFile` → `"Importing: …"` (`client/tag.go:151`);
- `ImportDir` and `Tag` → `"tagging <hash>"` once per file (`:565`, `:770`; the
  single-file path prints it twice);
- the template fallback message (`:347`).

It also writes ad-hoc warnings to `os.Stderr` (`reconcile: versioning …`, the
classify warnings, the duplicate-UID warning in `DirUIDFromPath`). A GUI
embedder (tie-fm) can't silence or capture either, and `tie import … | …`
pipelines get noise on stdout.

**Recommendation:** libraries never touch `os.Stdout`/`os.Stderr`. Give
`TieClient` a `Logger *slog.Logger` (default: discard) plus the typed progress
callbacks that already exist on `AlbumImportOptions`, `RepairOptions` and
`RescanOptions`. The CLI wires stderr and honours `-q` / `-v`. The import
command prints *data* to stdout: `hash<TAB>virtual-path` per imported item, so
`tie import … | cut -f1` works the way `tie upload` already does.

### 6.2 The CLI mixes data and chatter on stdout
- `tie import`: `"Uploading to [default]"` (`commands.go:1143`, `:1236`),
  `"scanning …"`, `"[3/10] …"` and the album table all go to stdout.
- `tie mount`: `"Mounted … (Ctrl-C to unmount)"`, `"Unmounting"` and unmount
  errors go to stdout (`:709-715`).
- `tie version`: an unreachable server prints `unavailable` to **stdout** and
  exits 0.
- `media index`/`backfill`: summary counts go to stdout in an ad-hoc
  `key=value` style mixed with TSV.

**Recommendation:** one rule, enforced in review: *stdout carries only the
command's result, in a stable format; everything else goes to stderr.* `tie
version` should exit non-zero when a configured server is unreachable (that's
what makes it a scriptable health check) and print
`component<TAB>version<TAB>url` rows.

### 6.3 Interactive prompts read stdin unconditionally
`import --albums` prints `Import N albums? [y/N]` to **stdout** and reads
**stdin** (`commands.go:1276-1277`). With stdin from a pipe or `/dev/null` (cron,
`xargs`), the prompt is swallowed and the import silently aborts. Worse, a pipe
whose first line starts with `y` confirms it.

**Recommendation:** prompt on stderr and read from `/dev/tty`. If there's no
TTY, refuse with "use -y to confirm non-interactively" and exit 2.

### 6.4 One output format, deterministic, with escaping
- `printRows` iterates `map[string][]string`, so **row order differs between
  runs**, which breaks `diff`, `comm` and golden tests. Sort by relation (or use
  `sorted` order from the server) and keep value order.
- `dump` uses `encoding/csv` with `Comma='\t'`. That isn't TSV: fields
  containing `"`, CR/LF, or a leading space or tab are **quoted, with doubled
  quotes**. `restore` round-trips it, but `cut`/`awk`/`sort` consumers see
  `"..."` artefacts, and the Usage text promises plain TSV.
- `--json` exists on `upload` and `stat` only.

**Recommendation:** define one text format for every listing command:
"IANA TSV with backslash escapes", i.e. `\t`, `\n`, `\\`, the PostgreSQL
`COPY` text style. It needs one shared encoder in `cmd/tie/format.go` and is
used by `dump`, `get`, `tag files`, `versions list`, `history`, the `verify`
report and the `--fix` journal. Add a global `--format tsv|json|table`
(default `table` on a TTY, `tsv` otherwise, with a header row only for
`table`). `restore` and `repair apply` read the same TSV.

### 6.5 `verify` does too much and reports to the wrong stream
`verify` currently means: index check, tree scan, index repair, orphan
re-homing, five classes of destructive repair, journaling, and a re-verify.
That's an fsck, a repair planner and a repair executor behind one set of mutually
dependent flags (`--dry-run` only with `--fix`, `--fix` implies `--repair`,
`--index` short-circuits, `--deep` only for `--index` semantics…). Its findings
go to **stderr**, so the report — the command's actual product — can't be
piped.

**Recommendation (composable pipeline):**

```
tie verify [--check-blobs] [--deep]        # read-only; findings → stdout as TSV:
                                            #   kind  subject  detail
tie repair plan  [< findings.tsv]          # → stdout: op  key  relation  value  note
tie repair apply [< plan.tsv]              # applies, echoes the journal on stdout
```

`tie verify | tie repair plan | tee plan.tsv | tie repair apply` then replaces
`--fix --dry-run --journal`. The plan **is** the journal format, and a journal
can be replayed or reviewed with standard tools. Keep `verify --repair` as a
thin compatibility alias for a release.

Exit codes should follow fsck convention:
- `0` clean;
- `1` problems found;
- `2` usage error;
- `4` operational failure (server unreachable, partial report).

Today "problems found" and "could not run" are both `1`, so cron can't tell a
broken store from a broken network.

### 6.6 No surprising side effects
- **Any** `tie` invocation, including `tie --help`, `tie completion bash` and a
  typo, calls `LoadOrCreateConfig` and **writes a config file** into the user
  config dir when none exists (`cmd/tie/main.go:22`). Read-only commands
  shouldn't write. Recommendation: only `tie conf create` writes. Otherwise print
  "no config; run `tie conf create`" and continue with defaults for commands
  that don't need one.
- The default `-C config.toml` is searched in the **current directory** first,
  so the same command behaves differently depending on `cwd`. Document the
  precedence and add `TIE_CONFIG` (env) as the conventional override.
- `conf`'s alias `c` reads like the global `-c` (collection). Drop the alias.

### 6.7 Arguments and globbing
- `tie del key '*' '*'`: `*` is a **shell glob**. Unquoted in a non-empty
  directory, it expands to filenames, and the command deletes
  `(key, file1, file2)` or prints a hint and exits 0. Replace the wildcards with
  flags: `tie del key --all`, `tie del key --relation R`.
- `tie get a -b` uses a leading `-` for exclusion, which collides with flag
  parsing (`-b` is parsed as an unknown flag) unless the user writes `--`. Use
  `--not b` (repeatable) or `!b`.
- Commands accept keys only as arguments. Accepting `-` meaning "read keys from
  stdin, one per line" (`tag set`, `stat`, `get`, `download`) makes
  `tie tag files x | cut -f1 | tie tag add-to y -` possible without `xargs`
  quoting issues.

### 6.8 Errors and quietness
- Errors print as `tie: Add Error: …`, `tie: Error: Config not loaded`, with
  inconsistent capitalization and doubled "Error". Use Go-style lower-case
  messages, prefixed `tie <command>:` by `main`.
- Progress bars render to stderr even when stderr isn't a TTY, filling cron
  logs with carriage-return frames. Disable them when `!isatty(stderr)` or with
  `-q`.

### 6.9 Small things that are already right and worth keeping
- `printRows` switches table/TSV on `isatty(stdout)`.
- `restore` reads stdin when no file is given.
- `verify` exits non-zero on problems.
- Servers log to stderr (pretty) or a file (JSON), are configured by file,
  answer `-version`, and shut down cleanly on SIGTERM.
- `filehost` stays content-agnostic (sniffing Content-Type from magic bytes is
  serving metadata, not parsing).

---

## 7. Refactoring options

Each option lists the alternatives considered and a recommendation. They're
ordered by value per unit of effort.

### 7.1 Filehost: a `server` struct and one hardened upload path (recommended)

Today the filehost is package globals (`stores`, `defaultStore`, `filecache`,
`key`, `listenOn`) plus five handlers that each re-implement "parse hash →
validate (badly) → find store → maybe cache → serve".

- **Option A (recommended).**
  - `type server struct { stores []*blobStore; def *blobStore; cache *blobCache; auth *auth.Store }`
    with methods for the handlers.
  - A `withHash(h func(w, r, hash string, st *blobStore))` adapter that does
    `IsHexHash`, `findBlob` and the 404 in one place (it closes §2.1 by
    construction).
  - `putBlob(st, r.Body, wantHash) (hash string, existed bool, err error)`,
    implementing §2.3 (temp-in-dir, hash while copying, fsync, link/rename),
    shared by `/upload` and `/upload/{hash}`.
  - Proper status codes throughout.
  - Tests become `httptest` against a `server` built from a temp dir, with no
    global resets.
- **Option B.** Patch in place: add `IsHexHash` checks in each handler. That
  closes the hole, but it leaves the duplication that caused it.

### 7.2 Client: collection-scoped handles (recommended)

Replace "bound collection + optional `collection string` parameter" (§3.8) with:

```go
func (tc *TieClient) In(name string) *TieClient   // resolved via Config.ResolveCollection
```

- **Removes:**
  - `QueryIn`, `ExpandIn`, `GetIn`, `SyncIn`, `NewBatchIn`, `collectionInfo(name)`;
  - every `collection string` parameter on `ImportDir`, `ImportFile`,
    `WriteFile*`, `Tag`, `tagDir`, `detachChild`, `reconcileDir`,
    `supersedeToPrev`, `Verify`, `RepairOrphans`, `RepairTree`,
    `IndexCollection` (`IndexOptions.Collection`), `ensureAddedAtIn`, `allOfType`,
    `childRows`, `collectionMembersIn`;
  - the `prevCollectionFor(main)` special-casing, which becomes
    `tc.In(tc.active.Collection + "_prev")`.
- **Gains:** correct by construction, and fewer parameters on every signature.
- **Cost:** a mechanical change across `client/`, `cmd/tie`, `io/tiefs`, and
  tie-fm (external).
- **Alternative:** keep the parameter but thread it everywhere, through
  `MkTieDirAll`, `DirUIDFromPath`, `fileChildren`, `Get`… That's a larger diff
  for a worse API.

### 7.3 tiedb: lock discipline and file layout

`collection.go` (1137 lines) mixes trie interning, association indexing, the
arena, slot allocation, query algebra, sorting and pagination.

- Split it into:
  - `intern.go` (levels, `insert`, `getEntryFromString`, `getValue`);
  - `assoc.go` (insert/delete association, `putAssoc`, `resolveEntry`);
  - `slots.go` (`allocSlot`, `freeSlot`, arena);
  - `query.go` (`TagQuery`, `CoTags`, `filterMissingRelation`);
  - `sort.go` (`Sort`, `GetPage`, `SortOptions`).
  This is a pure move with no behaviour change; do it first, as its own commit.
- **Iteration rule:** `lockedTree.ForEach` and `AssociationSet.ForEach` take a
  snapshot under the lock and call `fn` with no lock held (§2.2, §4.4). Document
  it as the rule for every container in the package.
- **Write rule:** every mutation runs under `changeMutex` and enqueues before it
  indexes (§4.3). Internal `*Locked` helpers let `Update`/`SetValues`/batches
  compose atomically (§4.13).
- **Options for concurrency granularity:**
  - (a) **(recommended)** keep the single `changeMutex` per collection. Writes
    are already serialized through one writer goroutine, so finer locking buys
    little and costs correctness.
  - (b) per-subject lock striping. Only worth it with a benchmark showing
    `changeMutex` contention.

### 7.4 One write primitive; atomic batches

`Add`, `Delete`, `Update`, `UpdateAdd`, `SimpleUpdate` and `SetValues` are six
entry points with subtly different locking and messages. The API layer exposes
four single-op requests plus `Batch`.

- **Recommended:** `func (c *Collection) Apply(ops []Op) (applied int, err error)`
  runs all ops under one `changeMutex` hold, with all-or-nothing validation up
  front (e.g. an `update` whose source triple is missing fails the batch before
  anything is applied). `Add`/`Delete`/`Set` become one-op wrappers. On the wire,
  keep the single-op requests as deprecated aliases that build a one-op `Batch`.
- This also gives the server-side compare-and-set needed for atomic `MkDir`
  (§4.10): `Op{Kind: "add-if-absent", Key: path, Relation: "path", …}`.

### 7.5 API layer: from copy-paste files to a typed registry

Each of the 15 request types repeats the same ~40 lines (`Id`,
`ReplyStructPtr interface{}`, `New()`, `NewXRequest` constructor), and several
files still carry the "To create a new request type: copy this file…"
template. Copy-paste is how §5.1-L1 (`OrigValue1 = Value2`) got into three files.
The read/write classification lives in a separate map (`readRequests`) that a new
request can silently fall out of (safely, as "write", but still).

- **Recommended:**

```go
type Handler[Req, Rep any] func(env *Environment, req *Req) (*Rep, error)
func Register[Req, Rep any](ws *Webservice, id string, access auth.Access, h Handler[Req, Rep])
```

  The registry owns decoding into a fresh `Req`, the access check, encoding and
  status codes. Request structs become plain data. The client side is a matching
  generic `Call[Req, Rep](c, id, req)`. The wire format is unchanged.
- **Alternative:** move to an established RPC (Connect/gRPC/JSON-RPC). That's
  more machinery than this project wants; the generic registry is about 80 lines.

### 7.6 CLI structure

- **Split `commands.go`** (1473 lines) into one file per command group
  (`cmd_tag.go`, `cmd_verify.go`, …). `media.go` already does this.
- **One `Before` hook** that fails with exit 2 when the config didn't load,
  replacing the 30+ copies of `if tie == nil { return errors.New("Error: Config not loaded") }`.
- **One `format.go`** with the TSV/JSON/table writer (§6.4) and one `progress.go`
  that builds the TTY-aware progress bar (it's currently configured twice,
  identically, in upload and download).
- **No global `tie` variable.** Pass an `app` struct through
  `cmd.Metadata` / closures. The global is why the command tree has to be built
  after a pre-parse of `os.Args` (`parseGlobalFlags`). Generating dir-type
  subcommands from config is the only reason for that dance. Consider
  `tie import --type audio-dir` (one subcommand, with completion from config)
  instead of one subcommand per type: it removes the pre-parse and makes `-C`
  behave like any other flag.

### 7.7 One single-flight disk cache, shared by filehost and tiefs

`cmd/tie-filehost/cache.go` and `io/tiefs/cache.go` are the same design written
twice (its own header comment says it "mirrors" the other), with the same FIFO
eviction and diverging bugs (§3.2, §4.16).

- **Recommended:** `internal/diskcache` with `Get(key, produce func(w io.Writer) error) (*Handle, error)`,
  refcounted handles (`ReadAt`, `Close`), true LRU, a size budget, and optional
  start-up adoption of an existing directory. tiefs adds its HTTP producer and
  archive-member producer; the filehost adds a copy-from-store producer.

### 7.8 Server bootstrap helper

The three server `main`s each re-implement:
- the `User{Username,Password,Role}` struct;
- the role-parsing loop and the `ParseAnonAccess` call;
- `-config` and `-version` handling;
- `tielog.Setup` and `LogVersion`;
- signal handling.

Move `UserConfig` and `func BuildStore(users []UserConfig, anon string, def Role) (*Store, error)`
into `auth`, and add a tiny `internal/daemon` (`ParseFlags(name)`,
`LoadConfig(&cfg)`, `WaitForSignal()`). That's about 150 lines removed, and the
three servers stay consistent (e.g. the duplicate-user and empty-password checks
from §5.1-L18 land once).

### 7.9 Import pipeline: one engine, two front-ends

`ImportDir` (≈190 lines) and `importAlbumFiles` (≈110 lines) duplicate:
- the batch/flush closure;
- directory-chain creation with `MkTieDirAll`;
- `albumMeta` aggregation;
- per-file classify/extract/tag;
- `SetDirType` + `IndexCollection` at the end.

They differ in two ways: where file placement comes from (a filesystem walk or
`SubPaths`), and whether to reconcile.

- **Recommended:** `importPlan{root string; items []planItem{src, rel string}; reconcile bool}`
  plus one `executeImport(plan)` that does upload (with HEAD-skip, §5.2-2),
  single-open ingest (§5.2-3), batched tagging, optional reconcile and
  dir-type/index. `ImportDir`, `ImportFile` and `ImportAlbums` become plan
  builders, and the `--dry-run` for plain `import` comes for free.

### 7.10 One vocabulary for relation names

There are two parallel vocabularies:
- the stringer-generated `TieProperty` enum, used through `str(TieTag)`, which
  needs `go generate` to extend;
- raw string constants (`RelGenre`, `RelDisc`, … in `media.go`) plus inline
  literals (`"album-artist"`, `"duration"`, `"types"`), added precisely *because*
  extending the enum requires regenerating.

**Recommended:** plain typed string constants for everything
(`const RelTag Relation = "tag"`). Drop `str()`, the `tieproperty_string.go`
stringer and the inverted lookup map. It's a mechanical rename, with no change
to stored data. Keep `TieType` as is, or convert it the same way.

### 7.11 Split `client/tag.go` (1983 lines)

It currently holds:
- type definitions and the enum;
- import (`ImportFile`/`ImportDir`, templates, aggregation);
- reconcile/versioning glue;
- the directory tree (`MkTieDir*`, `ReadTieDir`, `Rename*`, `Delete*`);
- tag management;
- tag queries;
- media relations;
- dir-types.

Split along those lines (`types.go`, `import.go`, `tree.go`, `tags.go`,
`dirtypes.go`, `relations.go`). Pure move.

---

## 8. Simplification options

These are concrete deletions and collapses, each small enough for one commit.

| # | Simplification | Removes | Risk |
|---|----------------|---------|------|
| S1 | Drop every client `Sync()` that directly follows a `Batch()` (the server already syncs inside `Batch`, and reads are read-your-writes regardless); once §4.2 lands, keep `Sync` only as an explicit durability call | ~20 round-trips per import/rename/delete path, ~40 lines | None for correctness (verify with the client suite) |
| S2 | `Sort`: replace the channel + goroutine + 1000-cap slice with a direct `ForEach` append into a `Size()`-sized slice | `loadTriples`, 533 KB per call | Low |
| S3 | `ForEachTriple`: use the set `ForEach` hands you | The deadlock (§2.2), one lookup per subject | None |
| S4 | Enqueue-before-index in `Add` (§4.3), then delete the `readSlot` retry/sleep loop | ~25 lines of retry logic and its 1 s worst-case stall of the writer | Low; covered by existing read-your-writes tests |
| S5 | `CoTags` (`tiedb/cotags.go`) duplicates `QueryTags`' Include/Exclude/Scope algebra and lacks `MissingRelation`. Extract `func (ic *Collection) resolveQuery(q TagQuery) (*AssociationSet, found bool)` and use it in both | ~35 duplicated lines; CoTags gains MissingRelation for free | None |
| S6 | Three HEAD-a-blob implementations (`blobExists`, `StatBlob` in `client/verify.go`, `repairSession.statBlob`) and two GET helpers (`fetchAudioMeta`, getlib). Introduce a small `FileHostClient{URL, HTTP, Store}` with `Stat`, `Get`, `GetRange`, `Put`, `Version`; `putlib`/`getlib` become its implementation | ~120 lines; one place for status-code handling | Low |
| S7 | Record encoders `EntryToBytes`/`HashToBytes`/`toBytes`: write into one `[ENTRY_SIZE]byte` at fixed offsets, mirroring the decoders | ~50 lines, 6–8 allocations per record | Low; the on-disk format is unchanged, so add a golden test first |
| S8 | Dead code: `FileIndexer`; `tiedb.Debug`/`Info` wrappers; `ReadConfig`; `GetVideoHeight`; `PathToWorkdir`; filehost `AddressOf`/`AddressOfFile`/`InitKey`/`key`; `client.InitKey`/`Config.key`/`tieKey`; unused `db`/`dbPath` globals; `tie add --gallery-name/--tags`; `Environment.Account` + `Webservice.GetAccount` + `UserNamespace` (no request uses them); stale "copy this file" templates in `api/*.go` | ~200 lines | None |
| S9 | Result shapes: tiedb has `TripleSet` (3-level nested maps), `[]StringTriple` and `[]Row`. `TripleSet` is used only by `Get`, `SetValues`/`SimpleUpdate`, `Associated` and `GetTripleSet`/`GetPage`. Make `Get` return values for one relation (`[]string`), drop `TripleSet`, `Value1`, `Value2`, `Unit` and their helpers, and have `GetPage` return only the slice | ~150 lines and a lot of map allocation | Low; `Associated` reply shape changes, so keep a converter there or deprecate `Associated` in favour of `Query{Reverse, Expand}` |
| S10 | `Config.Webservice` alias + `normalizeConfig`: announce removal in the CHANGELOG and drop it after one release | Two-way copying, one source of confusion | Low |
| S11 | Unify `ResolveHost(name)` and `ResolveHosts(collection, explicit)`; after §7.2 the latter loses its `collection` argument | One concept | Low |
| S12 | `printIndexReport`/`printVerifyReport` become the TSV encoder from §6.4 plus a table renderer | ~90 lines of hand formatting | Low |
| S13 | `examples/` builds, but documents APIs (`TripleSet`, raw `ws` usage) that this review proposes to change; either keep it compiling in CI or delete it in favour of `client` package examples (`Example*` functions in `_test.go`, which are compiled and run) | Drift | None |

---

## 9. Risk map: where bugs are likely to live

| Area | Why it's risky | Guard-rails to add |
|------|----------------|--------------------|
| `tiedb` locking (`lockedtree.go`, `associationset.go`, `collection.go`) | Three lock layers (`changeMutex`, shard RW locks, set RW locks) plus a writer goroutine; callbacks run under locks; the history of lost-update and resurrection bugs | The iteration rule (§7.3); a `-race` stress test that runs Add/Delete/Query/Dump/CheckIndex concurrently for 10 s in CI; a lock-order comment at the top of `collection.go` |
| `tiedb` durability (`filehandling.go`) | Acknowledge-before-write, panic on I/O error, global WaitGroup "Sync", slot reuse | Barrier sync (§4.2); a crash-injection test (truncate the file at random offsets, reload, check invariants); an `ENOSPC` test via a tiny loop-mounted FS or a fault-injecting `io.WriterAt` |
| Filehost handlers | Unvalidated path input, non-atomic writes, 200-on-error, global state | §7.1; fuzz `{hash}` routes with `testing/F` over arbitrary byte strings, asserting that the resolved path stays under the store root |
| Reaper | Destructive, time-based, cross-store, TOCTOU | Re-check under lock (§4.7); a dry-run mode that logs what *would* be removed; a test that interleaves `recordUpload` with a sweep |
| Client tree operations (`MkTieDir`, `RenameDir`, `reconcileDir`, `detachChild`, `supersedeToPrev`) | Multi-request read-modify-write sequences with no server-side atomicity; the hidden 1000 cap; mixed collections | §3.1, §3.8, §7.4; property tests: random sequences of mkdir/rename/delete/import against a memory-mode server, then `Verify()` must report zero problems |
| `verify --fix` / `RepairTree` | Destructive and heuristic ("ghost" detection depends on HEAD results and on query completeness) | Refuse to `--fix` when the index check reported problems in the same run; journal after apply (§5.1-L16); the composable plan/apply split (§6.5) |
| Blob cache (`io/tiefs/cache.go`) | Refcount-free sharing of fds | §3.2; a test that reads A, evicts it via B, and reads A again |
| Album planner (`client/albums.go`) | Large heuristic surface (disc detection, merge/split, templates) | It's already the best-tested file (`albums_test.go`); keep `--dry-run` as the default UX for new libraries |
| Config resolution (`client.Config`) | Two URL keys, flat vs `[Collections]`, `-c` vs `--collection`, cwd search | One `tie conf show` command that prints the *resolved* collection/hosts/URL for the given flags, which makes misconfiguration visible |

---

## 10. Tooling and process

- **`staticcheck` is stale.** The installed binary can't read Go 1.27 export data.
  `go install honnef.co/go/tools/cmd/staticcheck@latest` and add it, together
  with `go vet` and `govulncheck`, to a `make check` target.
- **The Makefile has no `test` or `check` target.** Add:
  ```make
  check: ; go vet ./... && staticcheck ./... && go test -race ./...
  ```
- **`make push` is dangerous.** It runs `go get -u .` (unreviewed dependency
  upgrades) and `git add -A`, which would commit the currently untracked
  `.tmp-hashprobe/`, `.opencode/`, `opencode.json` and `AGENTS.md`. Drop
  `go get -u` (make it a separate `make deps-update`) and stage explicitly, or
  at least add the scratch paths to `.gitignore`.
- **The client test suite needs a live stack** on `:2161`, and skips silently
  when it's absent (`requireServer`). Options:
  - (a) **(recommended)** start an in-process triplestore with
    `tiedb.NewDB(false)` behind `httptest.NewServer(ws.mux)` in `TestMain`, plus
    an in-process filehost on a temp dir. The suite then always runs, in CI and
    in parallel, without touching anybody's sandbox.
  - (b) keep the external stack but **fail** instead of skipping when
    `TIE_E2E=1`.
- **There is no CI config in the repo.** A minimal workflow running
  `make check` on push would have caught §5.1-L1 (if a reply-echo test existed)
  and would run the race stress test from §9.
- **Docs drift.** CLAUDE.md places the FUSE cache in `io/fuselib/fuselib.go`
  (it's in `io/tiefs/cache.go`), says eviction keeps readers working (§3.2), and
  calls the caches LRU (they're FIFO). Update after the fixes.

### Test gaps worth closing first

1. Filehost: traversal on every route (§2.1); a concurrent same-hash upload; a
   mismatch returning non-2xx; a crash mid-upload leaving no blob.
2. tiedb: dump concurrent with adds (§2.2); `CheckIndex` with a dangling id
   (§4.1); `Sort` allocations as a benchmark with an `AllocsPerRun` budget.
3. client: a directory with 1500 children (§3.1); `import --collection X`
   leaving `verify` clean in X (§3.8); `name` for `map.mp3` (§3.6).
4. tiefs: eviction while a handle is open (§3.2); commit failure keeps the
   staged bytes (§4.9).

---

## 11. Suggested roadmap

**Week 1: safety (small diffs, high value)**
1. §2.1 hash validation on every filehost route, plus a fuzz test.
2. §2.2 `ForEachTriple` one-line fix, plus a regression test.
3. §2.3 / §3.3 / §3.4 atomic upload (temp-in-dir, fsync, link), proper status codes.
4. §3.6 `TrimSuffix`, plus a backfill of `name`.
5. §3.7 name validation and read-doesn't-create on the triplestore.
6. §4.1 nil-safe `getValue`.

**Week 2: correctness**
7. §3.1 "0 = unlimited" normalization, plus a 1500-children test.
8. §3.2 refcounted, true-LRU blob cache.
9. §3.8 / §7.2 `tc.In(name)` scoped clients.
10. §4.2 barrier `Sync`, and drop the redundant client `Sync`s (S1).
11. §4.3 enqueue-before-index, and delete the `readSlot` spin (S4).
12. §4.4, §4.5, §4.7, §4.9.

**Week 3: performance and Unix fit**
13. §3.5 `Sort` without the channel (S2), with a benchmark.
14. §5.2-2/3 HEAD-skip uploads and single-open ingest.
15. §6.1–6.4 stdout discipline, deterministic TSV with escaping, `--format`, a
    `/dev/tty` prompt.
16. §6.5 `verify` / `repair plan` / `repair apply` split, with fsck exit codes.

**Later: structure**
17. §7.1, §7.3 (file split), §7.5, §7.6, §7.7, §7.8, §7.9, §7.10, §7.11, plus
    the S-items. Each is mechanical and best done as its own commit, with
    behaviour unchanged and tests green before and after.

---

## Appendix A — Reproducers

None of these were committed; they were run from a scratch directory or as a
temporary `_test.go` file that was deleted afterwards.

### A.1 ServeMux unescapes `%2F` inside a wildcard

```go
mux.HandleFunc("GET /{hash}", func(w http.ResponseWriter, r *http.Request) {
    h := r.PathValue("hash")
    fmt.Fprintf(w, "len=%d value=%q path=%s", len(h), h, PathFromHash("/srv/blobs", h))
})
req := httptest.NewRequest("GET", "/"+strings.Repeat("..%2F", 18)+"etc%2Fpasswd", nil)
// → 200 len=64 value="../../../../../../../../../../../../../../../../../../etc/passwd" path=/etc/passwd
```

### A.2 ForEachTriple deadlock

Memory-mode collection with 20 000 subjects; one goroutine `Add`s new subjects in
a loop, and another runs `ForEachTriple`. The writer stopped after 13 adds. The
goroutine dump:

```
sync.(*RWMutex).RLock                     rwmutex.go:74
tiedb.(*lockedTree).Get                   lockedtree.go:51
tiedb.(*Collection).getAssociations       collection.go:626
tiedb.(*Collection).ForEachTriple.func1.1 collection.go:1105
tiedb.(*lockedTree).ForEach               lockedtree.go:120     ← holds RLock
---
sync.(*RWMutex).Lock                      rwmutex.go:155
tiedb.(*lockedTree).GetOrPut              lockedtree.go:82
tiedb.(*Collection).insertAssociation     collection.go:601
tiedb.(*Collection).Add                   collection.go:165     ← holds changeMutex
```

### A.3 Cache eviction closes a live blob

`NewCache(srv.URL, srv.Client(), 5000, false)` with two 4 KiB blobs. Resolve
blob A, then resolve blob B (which evicts A), then read A:

```
read /tmp/tie-cache-…/eab9bba3…: file already closed
```

### A.4 Sort allocation

```go
col := NewDB(false).GetCollection(...); col.Add("k", "rel", "v")
// 1000 × col.ExpandKeys([]string{"k"}, "")
// → bytes allocated per single-key ExpandKeys (1 triple): 533059
```

### A.5 TrimRight

```
bigpig.jpg   TrimRight="bigpi" TrimSuffix="bigpig"
map.mp3      TrimRight="ma"    TrimSuffix="map"
```
