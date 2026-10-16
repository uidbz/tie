# tie-smb implementation plan (living handoff doc)

Goal: a standalone `tie-smb` server exposing the same live tree as
`tie mount --db` (`/query`, `/files`, `/tags`) over SMB2/3, pure Go, no Samba.

Decisions (approved by user):
- One share `tie` mirroring the mount root.
- Shared transport-agnostic core in `io/tiefs`; FUSE is a thin adapter.
- Delete support: `client.DeleteFile`/`client.DeleteDir` (done).
- v1 has full write parity with `mount --db` (create/write/rename/mkdir/
  delete via /files, tag editing via /tags).

## Library

- `github.com/go-filesystems/smb` v0.7.0 (BSD-3, pure Go, stdlib-only) +
  `github.com/go-filesystems/interface` v0.5.0 + `hostcopy` v0.1.0.
- Dialects 2.1/3.0/3.0.2, NTLMv2+SPNEGO, signing (HMAC-SHA256/AES-CMAC),
  byte-range locks, copychunk, srvsvc share enumeration. Verified by upstream
  against Windows 11, macOS, Linux kernel client. NOT implemented: SMB 3.1.1,
  encryption, oplocks/leases, ADS, lwssvc/lsarpc pipes.
- Server API: `smb.New()`, `srv.AddUser(user, pass)` / `AddUserHash(user,
  ntHash)`, `srv.SetName(...)`, `srv.Share(name, fsys, opts...)`,
  `srv.ListenAndServe(addr)` / `Serve(ln)`, `srv.Close()`.
  Share options: `smb.ReadOnly()`, `smb.AllowUsers(...)`, `smb.WriteUsers(...)`,
  `smb.Access(func(user string) (connect, write bool))`,
  `smb.WithCapacity(total, avail)` / `WithCapacityFunc`.
- Filesystem interface to implement (`github.com/go-filesystems/interface`):
  `Close`, `ReadFile(path)`, `ListDir(path)`, `Stat(path) (Stat{Mode uint16,
  Size uint64, Inode uint64})`, `WriteFile(path, data, perm)`,
  `ReadLink(path)`, `MkDir(path, perm)`, `DeleteFile(path)`, `DeleteDir(path)`,
  `Rename(oldPath, newPath)`.
  Optional (implement these!): `Opener.OpenFile(path) (File{ReadAt,Closer,
  Size})` and the returned File as `WritableFile{WriteAt, Truncate, Sync}`.
  DirEntry: `NewDirEntry(inode, name, fileType)`; Stat: `NewStat(mode, size,
  inode)`. fileType bytes: use `fs.FileMode`-style (`fs.ModeDir`/0) — check
  how smb lib maps DirEntry.FileType()/Stat.Mode() to SMB attrs in
  `attrs.go`/`dir.go` of the lib before wiring.
  ERROR CONTRACT (critical): wrap `io/fs.ErrNotExist` / `fs.ErrExist` /
  `fs.ErrPermission` with `%w` at path boundaries.
- go.mod `go` directive bumped 1.25.0 -> 1.27.1 (required by these libs;
  toolchain go1.27.1 is installed locally and everything builds).

## Done

### Phase 1 — `io/tiefs` (transport-agnostic core)
- `io/tiefs/tree.go`: `Store` (blob cache + inode numbering `InodeID(key)` +
  write-staging dir + uid/gid), `Tree{Store, Tie}` (`NewTree`,
  `NewTreeWithStore`), `ParseTagQuery`, `SavedQuery`, `QueryFiles` (runs
  query + `Disambiguate`), `TagList`, `TypeList`, `Disambiguate`,
  `DisambiguateFiles`, `SuffixName`, `BaseName`, `ChildPath`.
- `io/tiefs/cache.go`: `Cache` (`NewCache(filehostURL, httpClient, maxBytes,
  verify)`), `Blob(hash) (*Blob, error)` with STRICT io.ReaderAt (EOF at/past
  size), `Member(archiveHash, member)`, `ArchiveMembers(hash)`,
  `MemberKey(archiveHash, member)`.
- `io/tiefs/path.go`: `PathDir(slashPath)` (wraps `fs.ErrNotExist`),
  `Mkdir(parentSlash, name)` (wraps `fs.ErrExist`), `Rename(parentSlash, name,
  dstSlash, newName, replace bool)`, `DeleteFile(parentSlash, displayName)`,
  `DeleteDir(parentSlash, name)` (maps `ErrNotEmpty`), `destExists`.
- `io/tiefs/meta.go`: `MetaBinding{Load,Store}`, `TagsBinding(hash)`,
  `DirTypesBinding(uid)`, `RenderMeta`, `ParseLines`, `TypeInodeKey`,
  consts `TagsSelfName=".tags"`, `TagsTypeName=".type"`.
- `io/tiefs/write.go`: `WriteHandle` via `Tree.NewWriteHandle(parentSlash,
  name, existing client.File)`: `Seed/WriteAt/ReadAt/Truncate/Size/Dirty/
  File/Commit/Close`; commit = DirUIDFromPath -> MkTieDirAll if missing ->
  `Tie.WriteFile(host,"",parentUID,name,tmpPath,nil)`; idempotent.
- `io/tiefs/static.go`: `ReadmeText`, `ReadmeName/TagListName/TypeListName`.
- `io/tiefs/archive.go`: `ArchiveMemberViews` (`Display()`, `Member()`).
- `io/fuselib/*` rewritten as thin adapters (fuselib.go, fuselib_db.go,
  fuselib_db_tree.go, fuselib_db_write.go, fuselib_db_meta.go, archive.go).
  `TieDBFuse{tree *tiesfs.Tree, fuseTree *TieFuse}` share ONE Store via
  `NewTreeWithStore`. `errnoFor(err, fallback)` maps io/fs errors -> FUSE
  errnos (nil -> 0!).
- Tests moved: pure-fn + cache tests -> `io/tiefs/*_test.go`;
  `TestListContentClassifiesEntries` stays in fuselib.

### Phase 2 — deletes
- `client.DeleteFile(tie, hash, parent DirUID)` = `detachChild(tie,"",...)`:
  drops the (hash,"parent",parent) edge; if last parent, drops ALL per-hash
  triples (incl. tags) — same guard import reconciliation uses. Blob GC stays
  with filehost retention.
- `client.DeleteDir(tie, uid, parent DirUID)`: requires empty (error contains
  "not empty"), deletes parent edge + all (uid,"path") triples; drops the
  dir's own triples when last parent gone (no orphans for the checker).
- Tests: `client/delete_test.go` (live store via `TestingConfig()`, skips if
  unreachable — needs test-env up).
- FUSE `pathDir.Unlink`/`Rmdir` wired (ENOTEMPTY mapping) — verified live.

## Done (continued)

### Phase 3 — `io/smbfs` + `cmd/tie-smb` — DONE (verified 2026-10-16)
- `io/smbfs/{smbfs,handle,ops,list,escape}.go` per the design below;
  `cmd/tie-smb/{main.go,tie-smb.toml.example}` per the tie-filehost pattern.
- Deviation from the write-flow wording below: `dataHandle.Sync()` is a
  NO-OP commit-wise (the smb server calls Sync after EVERY WRITE request;
  committing per request would re-upload the whole staged file and version
  every intermediate state into `<name>_prev`). The commit happens on
  `Close`, which the server always performs — including on connection
  teardown (verified: server.go closes every `of.f` when a conn dies).
  `metaHandle.Sync` DOES commit (tiny files, last-writer-wins is fine).
- Two bugs found by the tests and fixed:
  1. `resolveTags` never descended directories — any `/tags/<a>/<b>` path
     was treated as a meta leaf. Now: pending -> directory (DirUIDFromPath,
     dirs win like the FUSE mount) -> meta leaf.
  2. `WriteFile` passed the full segments (incl. the `tags` view name) to
     `resolveMeta`, so the parent resolved as `/tags/<dir>` and every tag
     write failed with ENOENT. Now passes `segs[1:]`.
- Live smoke test with the real binary + `smbclient -p 4445`: ls/mkdir/put/
  rename/get/rm/rmdir round-trip (bytes cmp OK), tag edit via `tags/` seen
  in `query/<tag>/`, escaped name `we~3aird~2aname.txt` round-trips, reader
  gets `NT_STATUS_MEDIA_WRITE_PROTECTED`, SIGTERM logs "shutting down/bye"
  and exits (the lingering pgrep hit was the self-match gotcha below).

### Phase 4 — packaging — DONE
- Makefile `CMDS += tie-smb`; `contrib/systemd/tie-smb.service` (hardened +
  `AmbientCapabilities=CAP_NET_BIND_SERVICE`); `contrib/install.sh` builds/
  installs the binary + unit + config but deliberately does NOT enable
  tie-smb (opt-in: `systemctl enable --now tie-smb`); README table row;
  `docs/smb.md` (layout, escaping, roles, mount recipes, limits); CHANGELOG
  entry under Unreleased.

### Phase 5 — tests — DONE
- `io/smbfs/escape_test.go`: escape table + round-trip + transport
  invariants (no `:`/illegal bytes/trailing dot-space/reserved bases,
  collision-free) + literal-`~` handling.
- `io/smbfs/smbfs_test.go`: live-store (skip-if-down via `liveShare`) unit
  tests: view Stat/List/ReadFile, full CREATE flow (pending Stat/List ->
  OpenFile WritableFile -> commit), truncate-rewrite, lazy seed partial
  edit, whole-file fallback, rename, delete + ErrNotEmpty->ErrExist,
  read-only views -> ErrPermission, query view, meta tags/.tags/.type
  (incl. pending meta), escaped-name round-trip through resolve.
- `io/smbfs/integration_test.go`: go-smb2 client (test-only dep, now a
  direct require) against an in-process smb server on 127.0.0.1:0 —
  mount, list, README, mkdir, create/write, read-back, in-place edit,
  rename, tag edit visible in query view, delete, reader role refused;
  plus an over-the-wire escaping test.
- Test hygiene: `TestListContentClassifiesEntries` now closes its TieFuse
  (was leaking /tmp/tie-cache-* + tie-write-* dirs).

## Original plan (kept for reference)

### Phase 3 — `io/smbfs` + `cmd/tie-smb` (design settled — read "Phase 3 findings" at the bottom first)
- `io/smbfs`: `type Share struct { tree *tiesfs.Tree; mu sync.Mutex;
  pending map[string]*pendingWrite }` implementing filesystem.Filesystem +
  Opener + WritableFile over tiesfs. Path model mirrors
  `io/fuselib/fuselib_db.go` (dbRoot/tagQueryRoot/tagQueryDir/taggedDir/
  pathDir/tagsDir/metaFile/archiveDir) but path-based: root = `query` |
  `files` | `tags`. /query read-only (README, tags, types, saved queries,
  ad-hoc query dirs, taggedDir via ReadTieDir, archive dirs via
  `ArchiveMemberViews`); /files writable; /tags mirrors /files with writable
  meta leaves + `.tags`/`.type` per dir.
- Stat: `NewStat(mode,size,inode)`; mode is full POSIX — dirs `0o040755`,
  writable files `0o100644`, read-only files `0o100444` (lib sets READONLY
  attr when `mode&0o222==0`). inode = `tree.InodeID(key)` with fuselib's keys
  (hash, DirUID, `TypeInodeKey`, `MemberKey`, `path+"\x00"+name` for
  uncommitted creates). No mtime possible (Stat has no time field; lib
  reports "now") — skip TagDate.
- ListDir: query root = README/tags/types + saved queries; query dir =
  `tree.QueryFiles`; files/tags dirs = `tree.PathDir` +
  `tiesfs.DisambiguateFiles`; tags dirs add `.tags`/`.type`; archive dirs via
  `ArchiveMemberViews`. Display names escaped (see escaping below).
- WRITE FLOW (pending-handle design — REQUIRED by the lib's CREATE flow, see
  findings): `WriteFile(p,nil)` = create-or-truncate -> stage a
  `tree.NewWriteHandle` (NOT seeded), register in `pending`, forceCommit=true;
  do NOT commit an empty file. `Stat(p)` must see pending paths.
  `OpenFile(p)` returns the pending handle if any, else a LAZY handle: reads
  served from `tree.Cache().Blob`/`Member` until first WriteAt/Truncate
  (stage + Seed then), forceCommit=false -> commit only when Dirty.
  Sync/Close -> Commit (dirty||forceCommit) + drop from pending.
  `WriteFile(p,data)` (whole-file fallback) = handle+WriteAt+Commit+Close.
- Meta files: in-memory buffer handle; `WriteFile(p,nil)` -> pending empty
  buffer; OpenFile -> pending or buffer=`RenderMeta(Load())`; Sync/Close ->
  `Store(ParseLines(buf))`.
- Read-only files (query results, archive members, README, tags/types
  listings): plain File (NOT WritableFile); WriteFile/MkDir/Delete/Rename on
  them -> `fs.ErrPermission`.
- Rename: only within /files, `tree.Rename(parent,name,dst,newName,
  replace=true)` (lib ignores ReplaceIfExists). DeleteFile/DeleteDir ->
  tree.DeleteFile/DeleteDir; wrap `tiesfs.ErrNotEmpty` in `fs.ErrExist`
  (lib maps ErrExist -> OBJECT_NAME_COLLISION). ReadLink -> error (lib never
  calls it). MkDir -> tree.Mkdir, only under /files.
- NAME ESCAPING (mandatory, not cosmetic): the lib's `smbPathToFS` REJECTS
  any path containing `:` (ADS stream syntax), and SMB forbids
  `\ / : * ? " < > |`, trailing dot/space, reserved names
  (CON PRN AUX NUL COM1-9 LPT1-9, caseless). Reversible escape: each illegal
  char -> `~XX` (lowercase hex of the byte; `~` itself -> `~7e`); trailing
  dot/space -> `~2e`/`~20`; reserved names -> prefix `~`. Apply on ListDir
  display names, unescape per segment when resolving paths. Document it.
- Case: lib `resolveCase` tries exact Stat first, then a case-folded ListDir
  match; tie names are case-sensitive — document.
- `cmd/tie-smb/main.go`: mirror `cmd/tie-filehost` pattern: `-config` TOML
  (`tie-smb.toml` + `.example`), `tielog.Setup`, `tielog.LogVersion`,
  `version.Get`/`-version`, slog. Config keys: ListenOn (":445"), ServerName,
  TieConfig (client.LoadConfig path/name), Collection, Host (filehost name
  for `ResolveHost`), CacheSizeGB, Verify, ShareName ("tie"), LogFile,
  LogLevel, [[Users]] {Username,Password,Role} reusing `auth.ParseUserRole`;
  write users = RoleWrite. Wire: LoadConfig -> NewTieClientFor ->
  ResolveHost -> tiesfs.NewTree -> smbfs.NewShare -> smb.New + AddUser +
  SetName + Share(shareName, sh, smb.WriteUsers(write...)) + ListenAndServe
  (goroutine); SIGINT/SIGTERM -> srv.Close + tree.Close.
  Port 445 needs root or `AmbientCapabilities=CAP_NET_BIND_SERVICE`; support
  high port for testing (`mount -t cifs -o port=...`).

### Phase 4 — packaging
- Makefile: `CMDS += tie-smb`.
- contrib/systemd/tie-smb.service (harden like tie-filehost +
  AmbientCapabilities=CAP_NET_BIND_SERVICE); contrib/install.sh mention.
- README table + docs/smb.md (layout, escaping, roles, mount recipes for
  Windows/macOS/Linux, limits: no 3.1.1/encryption/ADS/oplocks).
- CHANGELOG entry.

### Phase 5 — tests
- Unit: smbfs path resolution/escaping against fake filehost (httptest) +
  live test-env triplestore (pattern: client/delete_test.go skip-if-down).
- Integration: `github.com/hirochachacha/go-smb2` (test-only dep, MIT,
  resolves on proxy) as SMB CLIENT: dial 127.0.0.1:high-port, login, mount
  share, ls/read/write/rename/delete round-trip.
- Manual: Windows Explorer / Finder / smbclient recipes in docs.

## Gotchas learned
- `errnoFor(nil)` must return 0 (caused a phantom EIO on successful renames).
- go-fuse pathDir inodes have no stable Ino -> same dir can appear as several
  kernel inodes; harmless but confusing in debug logs.
- test-env: `./test-env/build.sh`, `start.sh`, `seed.sh`, `stop.sh`; mount
  with `( setsid ./bin/tie -C config.toml mount --db mnt >>log 2>&1 </dev/null & )`
  (double-fork so the tool shell doesn't kill it). NEVER `pkill -f` a pattern
  that matches the tool's own bash -c line (self-kill -> hang); use `moun[t]`.
- Old test-env store has leftover `smoketest` dirs/files from manual tests
  (harmless; `seed.sh` is additive).
- `client.TieClient` receiver is `tie` not `tc` in tag.go; `ServerVersion()`
  is the connectivity probe.

## Phase 3 findings (verified by reading smb v0.7.0 + interface v0.5.0 source, 2026-10-16)

### interface v0.5.0 (`filesystem`)
- `Filesystem{Close; ReadFile(p); ListDir(p) []DirEntry; Stat(p) Stat;
  WriteFile(p, data, perm os.FileMode); ReadLink(p); MkDir(p, perm);
  DeleteFile(p); DeleteDir(p); Rename(old,new)}`.
- `Opener{OpenFile(p) (File,error)}`; `File{ReadAt; Close; Size}`;
  `WritableFile{File; WriteAt; Truncate(size); Sync()}`. ReadAt must follow
  io.ReaderAt to the letter (short read => non-nil err; off >= Size => 0,
  io.EOF). WritableFile.Size reflects own writes; Sync = commit durable.
- `NewDirEntry(inode,name,fileType)` (fileType: 1=file, 2=dir per memfs
  convention — but the smb server IGNORES FileType, it Stats every child),
  `NewStat(mode uint16, size, inode)`.
- Error contract: wrap `fs.ErrNotExist`/`fs.ErrExist`/`fs.ErrPermission` with
  `%w` at path boundaries (tiesfs already does).

### smb v0.7.0 server flow (file.go/dir.go/info.go/path.go/casefold.go/access.go)
- `smbPathToFS`: `\`->`/`, `path.Clean("/"+p)`, refuses `..` escapes AND any
  path containing `:` (ADS syntax). So escaped names must never contain `:`;
  the lib never sees `:`; unescape only inside smbfs.
- CREATE (`create()`): `resolveCase` first (exact Stat, else case-folded
  ListDir match). Then:
  - missing + disp in {CREATE, OPEN_IF, OVERWRITE_IF, SUPERSEDE}:
    optDirectoryFile -> `MkDir(p,0755)`, else -> `WriteFile(p, nil, 0644)`;
    then `Stat(p)` MUST succeed (hence pending paths must be Stat-able).
  - exists + disp in {OVERWRITE, OVERWRITE_IF, SUPERSEDE}:
    `WriteFile(p, nil, 0644)` = truncate; then Stat again.
  - exists + disp in {OPEN, OPEN_IF}: no WriteFile; straight to OpenFile.
  - disp=CREATE on existing -> OBJECT_NAME_COLLISION (lib-side).
  - non-dir + fs implements Opener -> `OpenFile(p)`; if the File implements
    WritableFile -> positional path.
- WRITE: with WritableFile -> `WriteAt(data,off)` + `Sync()` per request
  (no read-modify-write fallback used). FLUSH -> Sync. SET_INFO
  EndOfFile/Allocation -> `Truncate(size)` on the handle.
- SET_INFO FileDispositionInformation -> deleteOnClose flag; CLOSE ->
  `f.Close()` always, then DeleteFile/DeleteDir if deleteOnClose (full path).
- SET_INFO FileRenameInformation -> `Rename(oldPath,newPath)`; the lib
  IGNORES the ReplaceIfExists flag -> always replace=true.
- QUERY_DIRECTORY (`listDir`): `Stat(dir)`, `ListDir(dir)`, then
  `Stat(path.Join(dir, name))` for EVERY child — display names must be
  Stat-resolvable (escape must round-trip). Lib adds "."/".." itself and
  sorts by uppercased name.
- `Stat.Mode()` = full POSIX mode (memfs: dirs `0o040755`, files `0o100644`);
  `attributesOf` sets READONLY when `mode&0o222==0` -> use `0o100444` for
  read-only files so clients grey out writes.
- Stat has NO timestamps: `writeTimes()` reports now for all four FILETIMEs;
  TagDate mtime cannot be surfaced. `ReadLink` is never called by the server.
- `statusFor(err, fallback)`: ErrNotExist->OBJECT_NAME_NOT_FOUND,
  ErrExist->OBJECT_NAME_COLLISION, ErrPermission->ACCESS_DENIED,
  io.EOF->END_OF_FILE, disk-full errnos->DISK_FULL; else caller fallback
  (mostly ACCESS_DENIED). `tiesfs.ErrNotEmpty` is unmapped -> wrap in
  fs.ErrExist.
- Access: `WriteUsers(...)` -> non-writers get a read-only CONNECTION
  (writes refused MEDIA_WRITE_PROTECTED before reaching the fs);
  `Access(func(user)(connect,write))` is the dynamic variant.
- Share locking: write commands hold the share's exclusive lock for the whole
  command (CREATE incl. its OpenFile); reads share. So path calls are mostly
  serialized, but Stat/ReadFile/ListDir/OpenFile can interleave -> guard
  `pending` with our own mutex.
- Server API: `smb.New()`, `AddUser(user,pass)`, `AddUserHash(user,nt[16])`,
  `SetName`, `Share(name,fsys,opts...)`, `ListenAndServe(addr)`, `Serve(ln)`,
  `Close()`. Options: `ReadOnly()`, `AllowUsers`, `WriteUsers`, `Access`,
  `WithCapacity(total,avail)`, `WithCapacityFunc`.
- go-smb2 (hirochachacha) = test-only SMB client for phase 5 (MIT, resolves
  on proxy; not yet added to go.mod).
