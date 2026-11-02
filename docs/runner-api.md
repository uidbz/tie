# Runner API — the execution protocol

The runner daemon (`cmd/tie-runner`, and any compatible server) exposes an
HTTP JSON API for submitting batch jobs, sequencing multi-step pipelines,
deploying container images, administering long-running services, and
clustering runners into a fleet. This document is the normative
specification; the machine-readable counterparts are the `Route` table and
the golden fixtures in `testdata/runner/` (see `runner/api`).

## 1. Conventions

- **Transport.** HTTP/1.1; servers normally serve TLS. A plain-HTTP
  listener may additionally exist for the static fileserver (see
  `FileServer` config).
- **Bodies.** JSON, UTF-8. Request bodies are decoded leniently: unknown
  fields are ignored, so servers and clients of different versions can mix.
  **Field names are wire format** and most types have no `json` tags — the
  Go field name *is* the JSON name (PascalCase). Some fleet types carry
  lowercase tags (e.g. `"join_token"`, `"external_port"`); each fixture pins
  the exact spelling.
- **Errors.** Two shapes, per endpoint (pinned by fixtures):
  1. a zero-valued `JobStatus` carrying only `ErrorMsg`
     (`testdata/runner/job-zero.json`), used by the job/pipeline endpoints;
  2. plain-text `http.Error` bodies for fleet-registry and validation
     failures. Prefer shape 1 on `/api/*` data-plane endpoints.
- **Auth.** HTTP Basic. Three credential kinds:
  - **DeployKey** — per-image secret minted at deploy. Password auth; the
    username is checked only if the server config sets `AuthUser`
    (empty = accept any username). Gates: job cancel, pipeline
    cancel/resume (key of the entry step), image delete, service
    start/stop, deploy of an existing image.
  - **AdminToken** — shared fleet secret (`AdminToken` config; empty
    disables those endpoints entirely). Gates: `POST /api/reload`,
    `POST /api/cert`, fleet host listing/approve/remove, image replicate.
  - **FleetJoinToken** — seed-only secret. Gates `POST /api/fleet/register`.
- **`since` query parameter.** `GET /api/jobs`, `/api/pipelines`,
  `/api/stats` accept `since` as a Unix timestamp, a Go duration (`"720h"`),
  or a day count (`"30d"`). Filters `user` and `status` are exact matches.
- **Tie integration.** Content is exchanged by hash through the tie
  filehost (`Filehost` config + `Tie-Store` header). `GET /api/filehost`
  tells clients which URL and store to use. Input trees are tie directory
  manifests; step input selection follows `io/compose` (`Spec`): the
  selection vocabulary is byte-identical on both sides of the wire.
- **Version reporting.** `GET /api/capabilities` carries the server's
  `Version`, `GitCommit`, `BuildDate`. Clients gate behavior on it (and
  treat empty values as "predates version reporting").

## 2. Routes

The complete table (method, pattern, access) lives in `runner/api.Routes`
and `testdata/runner/routes.json`; it is kept equal by
`TestRoutesManifest`. Sections below document behavior. Fixtures in
`testdata/runner/` are exactly what a server marshals for each reply.

### run — POST /api/run

Request `JobRequest` (`job-request.json`) → reply `JobStatus`
(`job-status.json`). Access: public (the `User` field is self-reported
attribution). The runner may execute locally or forward the request to a
fleet peer whose capabilities fit; the reply always comes from the
responding runner with `DeployManager` naming the executing host.
`ForceAddToQueue` queues even when a local slot is free. `PipelineID`/
`StepName` are reserved for pipeline stepping.

### status — GET /api/status/{jobid}

→ `JobStatus`. Forwarded to the owning backend when the responding runner
does not hold the job. A not-found is a plain-text 404.

### jobs — GET /api/jobs, DELETE /api/jobs/{jobid}

`GET` → array of `JobStatus` (`jobs-list.json`), newest first, with `Log`
elided (fetch the log via `/api/status/{jobid}`). `DELETE` cancels; auth =
DeployKey of the job's handler image.

### pipelines — POST/GET /api/pipelines, GET/DELETE /api/pipelines/{id}, POST /api/pipelines/{id}/resume

A pipeline is a whole step graph submitted at once (`PipelineRequest`,
`pipeline-request.json`); the runner sequences it server-side. Steps name
each other via `compose.Spec.FromStep`; `Seeds` pre-satisfies a name with a
tree hash the client already uploaded. Status values are the job vocabulary
(pipeline level) and pending/running/done/failed/skipped (step level).
Replies are `Pipeline` (`pipeline.json`). `DELETE` cancels (DeployKey of
the entry step). `POST …/resume` accepts `ResumeRequest`
(`resume-request.json`) or `?from=`, with an empty body meaning "retry what
did not finish".

### queue-active-running — GET /api/queue, GET /api/active, GET /api/running

Queued jobs (array), the single active job (a bare `JobStatus` or `null`,
back-compat shape), and running jobs (array). Public.

### deploy — POST /api/deploymanager

Request `DeployRequest` (`deploy-request.json`), reply `DeployResponse`
(`deploy-response.json`). `Command` is `"test"` (build + test-run a job, no
image published) or `"prod"` (build, push, deploy). `ProjectHash` names a
tarball already uploaded to the filehost. Deploying a name that already
exists requires that image's DeployKey; a first deploy mints one and
returns it in `DeployKey`. `JobType` selects batch handler vs service
(`WebserviceV1`, `TCPServiceV1`); service fields configure ports and
autostart, and the assigned stable port comes back in
`ServiceExternalPort`.

### filehost — GET /api/filehost

→ `DeployResponse` with `Message` = filehost URL, `Store` = the configured
upload store (`filehost-reply.json`). Clients upload where the runner's
jobs will read from.

### fleet — GET /api/fleet

→ `FleetReply` (`fleet.json`): every runner URL known to the responder
(itself + configured `Backends`). This is how a client that knows one
runner discovers them all.

### fleet-registry — POST /api/fleet/register, GET /api/fleet/hosts, POST /api/fleet/approve, POST /api/fleet/remove

New runners join a fleet by repeatedly POST-ing `RegisterRequest`
(`register-request.json`, auth = `join_token`) to the seed until an admin
approves; the endpoint is idempotent per URL (polling is the join
protocol). `RegisterReply` (`register-reply.json`) returns `status`
(pending/active) and, once active, the seed's full `config` echo
(`config.json`) so the joiner can write its own configuration. Host
listing/approval/removal need the AdminToken; non-seed runners proxy these
to the seed. `FleetHost` entries (`fleet-hosts.json`): pending/active/
removed. `admin_token` is included in a listing only when it is meant to
bootstrap a joiner.

### cert — POST /api/cert

AdminToken. Body `{"cert": PEM, "key": PEM}`. The server installs the
pair and hot-swaps the live TLS listener (no restart); reply reports the
new subject and expiry. `GET /api/capabilities`' `CertNotAfter` is the
fleet-wide expiry monitor.

### stats — GET /api/stats

Query `since` (see conventions) and `local=true` to skip the fleet merge.
→ `JobStats` (`stats.json`). Servers merge peer stats; `ActiveUsers` maps
month → user list for cross-runner dedup and is omitted when empty.

### capabilities — GET-or-POST /api/capabilities

→ `CapabilityReply` (`capabilities.json`): job types, live resource pool
(free/total CPU/RAM/GPU, queue length, active count), build info,
`CertNotAfter`, `DeployedImageCount`. This is the routing input for
capacity-aware dispatch and the health-diff for admins.

### reload — POST /api/reload

AdminToken. Hot-reloads mutable config; `{"status":"reloaded"}`. Disabled
when `AdminToken` is empty. Signal-based reload works regardless.

### images — GET /api/images, PUT /api/images/replicate, DELETE /api/images/{image}

`GET` → `[]ImageInfo` (`images.json`) — never contains DeployKeys. `PUT`
(AdminToken) pushes one image entry fleet-wide (body = the image record).
`DELETE` (DeployKey) removes the image and stops its service if any.

### services — GET /api/services, GET/POST/DELETE /api/services/{service}

`GET` → map of `TaskInfo` keyed by service name (`services.json`); one
service via the named form. `POST` starts, `DELETE` stops (both
DeployKey). Statuses: running / stopped / crashed (crashed = died after
start; `logs` carries the reason).

### proxy — /service/{service}[/{params...}]

The data plane. Requests are forwarded to the service container with the
`/service/{name}` prefix stripped; responses are rewritten (HTML/XML/CSS/
JS URL rewriting, `Set-Cookie` path prefixing, `Location` rewriting) so
apps work under a shared origin. `ServiceProxyRewrite` (per image) limits
rewriting (`all` default, `html`, `none`) for backends that emit their own
prefixed URLs. WebSocket upgrade requests are tunneled. Clean-URL mode
keys off `X-Forwarded-Host`.

### files — GET /files/...

Static fileserver when enabled (client binaries, certificates). The
entrypoint binary name published here is configurable (default
`tie-exec`); container images fetch it at build time.

## 3. Container contract (entrypoint ↔ runner)

The runner executes handler images by invoking the entrypoint binary
(`cmd/tie-exec`) inside the container. This contract is frozen:

- Entrypoint flags: `-filehost URL -inputhash HASH [-store NAME]
  [-settings /settings.json] [-apphash HASH] [-debug] [-app NAME]
  [-args A...] [-input /input] [-output /output] [-prefix /tmp]
  [-usefuse] [-cache-size-gb N] [-version]`.
- Fixed container paths: input tree at `/input` (FUSE-mounted on demand or
  pre-downloaded), output collected from `/output`, settings file at
  `/settings.json`, injected app binary at `/app` (when `-apphash` set).
  The host-side tree directory bind-mounted into the container at `/ext`
  is configurable on the host side (`ExtDir`).
- **Result protocol on stdout.** Lines of the form

      #!TIE SetResult <output-hash>
      #!TIE SetError <message>
      #!TIE PercentDone <number>   (conventionally percent complete; the
                                   parser accepts any float, apps define the scale)

  Servers accept the canonical `#!TIE` token and (for compatibility with
  entrypoints deployed before this spec) the legacy spelling of the same
  protocol name. Entrypoints may emit both; servers ignore unrecognized
  prefixes. The child application's exit code propagates through the
  entrypoint.
- Progress may also be inferred by the runner sampling container memory;
  memory peak/current are reported in `JobStatus`.

## 4. Configuration echo and fleet join compatibility

`POST /api/fleet/register` echoes the seed's whole configuration
(`config.json` fixture, key set pinned by test). Rules:

- The echoed key set may grow; joiners must ignore keys they don't know.
- A key is never renamed or removed.
- Servers older than this spec may echo additional sections (e.g. cloud
  batch coordinator settings); core joiners ignore them.

## 5. Compatibility rules (normative)

1. Route patterns and methods are frozen. New routes are additive.
2. Exported field names in every type of `runner/api` are frozen forever,
   including values the current server does not populate (`BatchJobID`,
   `Queue`, `Region` are kept as the external-handle trio — servers and
   backends are free to repurpose the *values*, never the names).
3. Status vocabulary strings (`queued|running|done|failed|cancelled`,
   step `pending|…|skipped`, hosts `pending|active|removed`, tasks
   `running|stopped|crashed`) are frozen.
4. Job type names `CPUv1`, `GPUv1`, `WebserviceV1`, `TCPServiceV1` are
   frozen; deployments add their own names via config.
5. `io/compose` `Spec` field names are frozen wire format (the struct has
   no tags; its JSON spelling is its Go spelling).
6. In-container paths, entrypoint flags, and result-protocol line grammar
   (§3) are frozen.
7. Deprecation = stop emitting + keep parsing. Silent removal is a
   protocol break and requires a versioned fork, not a field edit.

Anything not pinned here or by a fixture is implementation detail and free
to change.
