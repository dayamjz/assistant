// Package taskstate answers what is true of a task now.
//
// PRD section 6 gives a task two records: an append-only event log of what
// happened, and a state record that is "authoritative, one owner" and resolved
// deterministically with its source attached. This package reads the second
// literally as a function. Every fact a task's state is made of already has an
// owner (the run row owns whether a validation is active or finished, the hold
// row whether a decision is open, the task row what is attributed to it, the
// event log what the worker said), so the state is computed from them on every
// read and nothing is stored. A stored copy would be the rewritten summary PRD
// section 8 names as the P14 failure, and the one that was stored,
// internal/store's task_state table, is sealed. Resolve is the precedence and
// its doc comment is the contract; kind.go is the one owner of what an event
// kind means.
//
// The package is read-only and takes no clock. Resolve is pure, and Reader
// only calls store accessors that read.
//
// # Where this departs from the PRD's wording
//
// PRD section 8's data model lists "task state" as an authoritative record.
// Here the record is the resolution rather than a row. That is a reading of
// the table, not an edit of it, and it is raised as a finding rather than
// settled in this package.
//
// # Residual gaps
//
//   - The terminal tier has no producer in this build: nothing here observes a
//     terminal, so every Reader built today passes a nil probe and the tier is
//     exercised by Resolve's tests alone. When a probe exists its answer is as
//     of the probe, taken after the records were read, and a terminal may
//     change afterwards.
//   - Attribution of a run to a task is by repository, branch and being no
//     older than the task (internal/store's TaskSnapshot). A task relaunched
//     in place can be outranked by an older run of the same branch until a
//     newer run exists, and nothing records an attempt boundary.
//   - The run mapping is the run's status and no finer: a failed and a
//     terminated run both read as Failed, and nothing here says why a run is in the
//     status it is in. It is pinned to
//     the run status vocabulary by a test, not reconciled with
//     internal/machine's outcome translation, which also reads a run's status
//     for a different audience.
//   - Only a run's own open hold supplies the detail of a Held answer. A hold
//     that names the task while its run is not held does not make the task
//     Held.
//   - Two tasks on one branch share their runs, so a newer same-branch run
//     made for another task can outrank a task's own run.
//   - Landed and Complete are not resolvable and are not declared States.
//   - A Reader loads a task's whole log for each read.
package taskstate
