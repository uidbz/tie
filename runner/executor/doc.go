// Package executor runs jobs. It holds the vocabulary of job types and
// their resource sizing, the container stdout marker protocol, and two
// execution paths:
//
//   - Local (the Docker runtime): `docker run` of the job image, whose
//     entrypoint binary receives the frozen CLI contract (-filehost,
//     -inputhash, ...). The container reports progress, errors, and its result hash on stdout
//     using the marker protocol; the runtime ingests it live, samples
//     memory, and reports the exit state.
//
//   - External (the deployment's own backend, e.g. a hosted batch
//     service): a Submit/Status/Cancel seam. The core package never
//     talks to any external backend itself; it defines the interface
//     the runner polls against and records the returned handle in the
//     job's ext-ref fields.
//
// The package owns no job registry, no store, and no queue: those live
// in the scheduler. Run updates the api.JobStatus it is handed and
// returns an ExitInfo; the caller finalizes.
package executor
