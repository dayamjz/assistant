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
// # Progress is derived here, from two facts no record holds
//
// PRD section 6's supervisor absorbs a wake only on positive evidence that
// work is proceeding, and Resolution.Progress is that evidence as this package
// can state it: a Validating answer from a run the service is executing now,
// or a Running answer from a terminal whose agent process is live. Both facts
// come from outside the store. Inputs.Advancing is the service's slot map,
// which internal/service hands NewReader; Inputs.Process is the session
// provider's probe. Neither is as of the snapshot: both are asked after the
// records were read, outside the store's transaction, and the gap is stated
// on NewReader. The one disagreement that matters, a slot that still says
// advancing while the record says the run settled, is no progress, because
// the record decided the state and the slot is not evidence about it.
//
// # Residual gaps
//
//   - The process tier has no producer in this build: no session provider
//     exists, so every Reader built today passes a nil probe and the tier is
//     exercised by Resolve's tests and a scripted probe alone. When a probe
//     exists its answer is as of the probe, taken after the records were
//     read, and a terminal may change afterwards. Live means the agent
//     process is running; a probe that cannot tell an agent from the shell
//     left in its pane would answer Live for a worker that has gone, which
//     is the probe's gap to close and not this package's.
//   - The advancing fact is as of the slot map, read after the snapshot, so
//     Progress can be false for a run that began advancing after the read
//     and the answer is as of neither moment exactly.
//   - Attribution of a run to a task is by repository, branch and being no
//     older than the task's latest attempt (internal/store's TaskSnapshot).
//     That closes the relaunch gap and keeps a queued task from inheriting
//     its blocker's runs, but two tasks whose attempts are both live on one
//     branch still share the runs made after the later attempt began.
//   - The terminal tier PRD section 6 puts above the run tier is not here.
//     The cleanup record reaches Resolve's caller on the snapshot and is not
//     read; the merge record waits on PR #113's shape, which is the seam
//     internal/store's package comment names; and Landed and Complete stay
//     undeclared until #107 reads both. The ordering rules this package
//     states for its own kinds, "relaunched" then "dispatched" and the
//     closed detail of "isolation-refused", are owed by their writers (#94
//     and #98) and checked by nothing here: the store appends any text, and
//     Row.Detail is the decoder a writer calls before appending, not a gate
//     at the append.
//   - Row.Writer says who may append a kind and refuses nothing itself. The
//     worker event path that reads it (#91) is the refusal; until it exists,
//     the column is a declaration a test pins and no mechanism enforces.
//   - The run mapping is the run's status and no finer: a failed and a
//     terminated run both read as Failed, and nothing here says why a run is in the
//     status it is in. It is pinned to
//     the run status vocabulary by a test, not reconciled with
//     internal/machine's outcome translation, which also reads a run's status
//     for a different audience.
//   - Only a run's own open hold supplies the detail of a Held answer. A hold
//     that names the task while its run is not held does not make the task
//     Held.
//   - A Reader loads a task's whole log for each read.
package taskstate
