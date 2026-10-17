# New-session prompt for tie-smb phase 3+ (paste into a fresh session)

Continue the tie-smb work in /home/johan/go/src/github/tie.

Read docs/tie-smb-plan.md FIRST — it is the full handoff: approved decisions,
what phases 1-2 changed (io/tiefs extraction, io/fuselib now thin adapters,
client.DeleteFile/DeleteDir, FUSE unlink/rmdir — all done, tests green), the
detailed remaining plan, the Gotchas section, and — most importantly — the
"Phase 3 findings" section at the bottom: verified facts read from the
go-filesystems smb v0.7.0 + interface v0.5.0 source (CREATE flow calls
WriteFile(p,nil) before OpenFile so staged writes need the pending-handle
design; smbPathToFS rejects ':' so name escaping is mandatory; Rename
ignores ReplaceIfExists; Stat mode conventions; error-contract mapping).
The Phase 3 design decisions are already settled there — implement them,
don't re-derive.

Your task: phase 3 — create io/smbfs (filesystem.Filesystem + Opener +
WritableFile over tiefs.Tree, mirroring io/fuselib/fuselib_db.go's
/query,/files,/tags semantics) and cmd/tie-smb (TOML config + tielog +
version + auth-roles pattern copied from cmd/tie-filehost; one share "tie";
WriteUsers from [[Users]] Role; SIGTERM graceful shutdown). Then phases 4-5
from the plan (Makefile CMDS, systemd unit, docs/smb.md, CHANGELOG; unit
tests + integration test using hirochachacha/go-smb2 as a test-only SMB
client against tie-smb on a high port).

Verify with: go build ./... && go test ./... (client delete tests and any
live tests need ./test-env/start.sh running; use the skip-if-down pattern
from client/delete_test.go). Follow the Gotchas section before touching
test-env mounts. Do not commit unless I ask.
