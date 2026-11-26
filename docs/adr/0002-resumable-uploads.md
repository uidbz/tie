# ADR 0002: Resumable uploads for large blobs

- Status: Proposed
- Issue: #160

## Context

`PUT /upload[/{hash}]` streams the body once. Over a lossy WAN link, a
multi-GB upload that fails at 90% costs a full re-upload. The dedup fast
path only helps when the blob is already complete on the server, not when
the transfer was cut off part-way.

## Options

1. **tus-style offset resume on the existing route.** `PUT /upload/{hash}`
   with `Tie-Upload-Offset: n` appends to a staging file kept for that
   hash. `HEAD /upload/{hash}` reports the staged length. The server
   verifies the full-body hash when the final byte arrives, then moves the
   file into the store.
2. **Chunked session.** `POST /upload/session` returns an id. The client
   uploads `PUT /upload/session/{id}/{n}` chunks in any order, then sends
   `POST .../commit {hash, size}`. The server concatenates the chunks,
   verifies the hash and stores the blob.
3. **Client-side chunking into tie directories.** Split big files into a
   manifest of chunk blobs. This needs no server change, but changes what
   a "file" is for every reader. Rejected.

## Decision (proposed)

Option 1, scoped to uploads that name their hash (`PUT /upload/{hash}`):

- The staging file is keyed by (store, hash) under the store root
  (`.staging/`), so a resume works across client restarts without a
  session id. Hash-less `PUT /upload` stays single-shot, because the
  server computes the address while copying and cannot resume an
  unknown target.
- The server checks the hash incrementally. On resume it re-hashes the
  staged prefix once, or keeps the hash state in a sidecar. A mismatch at
  the end deletes the staging file and returns 400.
- Staging files have a TTL (default 24 h) and are removed by the reaper.
  They count toward the store's disk but are never served.
- Concurrent writers to the same staging file are refused with 409
  (a file lock).
- `io/putlib` resumes automatically. On a transport error it sends
  `HEAD /upload/{hash}` and continues from the staged offset with backoff.

## Consequences

- New server state (staging) with its own cleanup path and disk
  accounting.
- Reverse proxies must pass `Tie-Upload-Offset` through and must not
  buffer whole request bodies.
- Old servers ignore the header and reject an offset upload with a hash
  mismatch. Clients detect support via `/-/version` and fall back to
  single-shot.
