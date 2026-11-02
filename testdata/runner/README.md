# Runner API fixtures

Golden JSON files pinning the wire protocol in `runner/api`. Each file is
exactly what a server marshals (every field present, Go marshaling order
irrelevant — the fixture test compares decoded trees).

- `job-zero.json` is the zero JobStatus (also the shape error replies use).
- `config.json` is the runner configuration echoed by
  `POST /api/fleet/register`; its top-level key set is pinned by the test.
  Older servers may additionally echo fields (`DefaultWebsite`, cloud batch
  coordinator settings); joiners must ignore unknown keys.
- Hostnames/names are fictional (example.com).
