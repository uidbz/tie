# Examples

These programs are part of the module, so `make check` (vet, staticcheck,
`go test -race ./...`) compiles them on every commit — an example that stops
matching the API fails CI rather than rotting quietly.

| directory | what it shows |
|---|---|
| `tiedb-client-add-get/` | the smallest useful client session: add triples, read a subject back |
| `tiedb-client-batch/` | many writes in one round trip, and the batch operations |
| `webservice/` | a server, a custom request type registered with `webservice.Register`, and a client calling it through `webservice.Call` |

Each directory carries its own `config.toml`; they expect the servers from
`test-env/` (`./start.sh`) to be running.

For the everyday API, prefer the runnable examples in the packages themselves —
`client/example_test.go` has `Example*` functions that `go test` compiles and
runs against a real triplestore, so they cannot drift from the signatures they
document.
