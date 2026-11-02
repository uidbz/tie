// Package protocoltest exercises the runner protocol end to end: the
// assembled runner/server (frozen route table, real HTTP transport)
// driven through the runclient library, with only the durable store and
// the container runtime faked. Client and server meet exclusively over
// the wire — the point is to catch drift between the two halves that the
// per-package suites cannot see: DTO marshalling, status semantics, auth
// headers, pagination-shaped list filters, and the cancel/pipeline
// authorization paths.
//
// It is deliberately assembled from the same exported API
// cmd/tie-runner uses, so it also doubles as an ergonomic check of the
// engine's composition surface.
package protocoltest
