// Package api defines the wire protocol between a runner daemon and its
// clients: the request/reply types behind every /api endpoint, the fleet
// join handshake, and the route table that pins the endpoint set.
//
// It is deliberately a leaf: it imports only the standard library and
// io/compose (whose Spec is embedded in pipeline step input selections), so
// clients, servers, and admin tools can all share one declaration of the
// contract. Historically the JSON contract was described twice — server
// types plus hand-mirrored client types — guarded by round-trip tests; this
// package is the single source of truth that replaces that arrangement.
//
// Fidelity rules, since the endpoint set predates this package:
//
//   - Field names are wire format. Most types here carry no json tags at
//     all: the wire spelling is the Go field name (PascalCase). Never rename
//     an exported field; add new fields instead (decoders ignore unknowns).
//   - Unexported runtime state does not belong here. This package describes
//     exactly what crosses the wire — nothing else.
//   - testdata/runner/*.json hold golden fixtures for every shape; the
//     fixture test round-trips each one. A change that breaks a fixture is a
//     protocol change and must be deliberate and versioned.
//
// The full endpoint documentation, including auth levels and error shapes,
// lives in docs/runner-api.md; the Route table below is its machine-readable
// mirror and is checked against testdata/runner/routes.json.
package api
