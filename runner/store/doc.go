// Package store keeps the runner's durable history: submitted jobs and
// pipeline runs, written on state transitions and read back after a restart
// or by the status and statistics endpoints.
//
// The Store interface is what the engine talks to; two implementations ship
// here:
//
//   - NewStore opens the embedded tiedb-backed store: one directory holding
//     three tiedb collections (jobs, pipelines, pipeline-steps). Every
//     persisted field is a (subject, relation, value) triple, and an upsert
//     is one Apply batch — a single critical section, so a crash can never
//     half-write a job.
//
//   - NewFake is an in-memory Store for tests of the engine and of
//     downstream code. It encodes through the same field map as the tiedb
//     store, so the two cannot drift on what a persisted job means.
//
// Schema (the field names are storage-internal, not wire-visible; the wire
// shape is runner/api):
//
//	jobs            key = job id;     one value per persisted field
//	pipelines       key = pipeline id; fields plus a multi-valued "step"
//	                                   relation listing step names
//	pipeline-steps  key = pipeline id, 0x1f, step name; one value per field
//
// Jobs are enumerated by the presence of their "created" relation, so the
// retention reaper and the list endpoints never need a full-collection scan
// beyond that. Numeric time values are stored zero-padded so lexicographic
// order equals numeric order.
//
// The store deliberately does NOT keep the in-memory hot cache — the job
// manager owns live state and writes here on the queued/running/terminal
// transitions only.
package store
