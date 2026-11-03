# Client and embedder notes: timeouts and cancellation

Two bounds apply to a request, and an embedder can set both.

## Triplestore requests

Triplestore requests carry a deadline (60 s by default). `TieClient.SetTimeout(d)`
sets it; a negative `d` means no timeout. Direct-file mode (`tie -f`,
`client.ConfigForFile`) applies the same bound to its in-process dispatches.

## Context variants

Every call has a `…Context` variant — `GetContext`, `QueryContext`,
`ReadTieDirContext`, `AddContext`, `SetContext`, `DeleteContext`, `BatchContext`,
`DownloadContext`, `UploadContext` (and `UploadOptions.Context`) — which abandons
work the caller no longer wants instead of running it to completion and throwing
the answer away.

Cancelling is **not a rollback**: what the server already applied stays applied.
A cancelled `Batch` may have applied some of its operations; the reply is simply
never seen. Design idempotent retries (adding an existing triple is a no-op)
rather than relying on cancel-to-undo.

## Filehost requests

Filehost requests are bounded by a response-header deadline (30 s by default,
`client.FileHostResponseHeaderTimeout`), so a host that accepts the connection and
then never answers — a half-dead `tie-filehost`, a captive portal, an NFS-backed
store on a sleeping server — surfaces as an error rather than a parked goroutine.
The timer is abandoned once headers arrive, so a 4 KB thumbnail and a 4 GB blob are
both unaffected: the bound is on the *first response*, not on the transfer.

The filehost layer carries the caller's context the same way
(`fhclient.StatContext`, `GetContext`, `PutContext`; `getlib.DownloadFileContext`).

## Distinguishing errors

Client methods return `(reply, error)`; `error` is non-nil for both transport
failures and API-level failures. `client.ErrNotFound` distinguishes "no such
key" from a real error — check it with `errors.Is`.
