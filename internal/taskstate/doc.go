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
// # The terminal tier
//
// PRD section 6 puts one tier above the run: a terminal record the program
// wrote once. Landed is derived from the merge record of a run attributed to
// the task, the record store.TransitionRunOnPullRequest writes in the
// transaction that ends the run, and Complete from that and the task's
// cleanup record with its removal marked. Both reach Resolve on the snapshot
// and nothing here writes either; the merge record's producer is
// internal/runs' PassOnMerge, reached from internal/service where the checks
// stage read the pull request as merged and from its checks-hold closure,
// and this package adds no reader of the code host. That closure runs at
// recovery and before a held run is resumed, so a merge that happens after
// checks_timeout elapsed is recorded when the service next looks, and a
// task reads Ready until then; the window is the closure's doc's to state
// (internal/service's checkshold.go), not a gap in the resolution. Whoever
// opened the pull request, the record is the proof: a pull request a
// person opened by hand on the task's branch, validated by a run attributed
// to the task and read as merged, lands the task. Where more than one
// attributed run has a merged record, the newest is the one named.
//
// A terminal record is final. Every run in the snapshot is read below it, so
// a run attributed later, by branch or named by the task's own row, does not
// displace it; work that continues after Landed is a new task, which is what
// internal/tasks' floor rests on when it treats Landed and Complete as
// cleared. No event kind is named landed or complete, and a worker's event by
// either name is a word with no row, which answers nothing (P8).
//
// Complete for an investigation task is not resolved in this build. PRD
// section 5's edge asks for a stored report and registered holds before the
// cleanup proof, and internal/store now holds both, the task_report row and
// the holds registered in its transaction, with internal/tasks' CleanUp
// verifying them before it writes the cleanup record; what this package
// does not yet do is read the report row, which TaskSnapshot does not carry.
// Rather than derive Complete from the cleanup record alone, the tier
// declines, and an investigation task with a removed copy stays Reported.
// Carrying the report onto the snapshot and reading it here is a change of
// its own, and the test that pins today's answer is the one that moves when
// it lands.
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
// records were read, outside the store's transaction, the slot before the
// probe, and the gap is stated on NewReader. The one disagreement that matters, a slot that still says
// advancing while the record says the run settled, is no progress, because
// the record decided the state and the slot is not evidence about it.
//
// # Residual gaps
//
//   - The process tier's producer is internal/session's Probe, reached
//     through internal/service's terminal seam by every Reader that service
//     builds, so a launched task reads Running from this tier on the task
//     surface while its worker runs, and Unknown from it once the worker is
//     gone with no run attributed. A probe's answer is as of the probe,
//     taken after the records were read, and a terminal may change
//     afterwards. Live means the agent process is running; a probe that
//     cannot tell an agent from the shell left in its pane would answer
//     Live for a worker that has gone, which is the probe's gap to close
//     and not this package's, and internal/session's doc says how its
//     provider answers it. A Reader built with a nil probe, which only a
//     service built outside Open is, observes no terminal at all.
//   - The advancing fact is as of the slot map, read after the snapshot, so
//     Progress can be false for a run that began advancing after the read
//     and the answer is as of neither moment exactly.
//   - Attribution of a run to a task is by repository, branch and being no
//     older than the task's latest attempt (internal/store's TaskSnapshot).
//     That closes the relaunch gap and keeps a queued task from inheriting
//     its blocker's runs, but two tasks whose attempts are both live on one
//     branch still share the runs made after the later attempt began.
//   - The cleanup record's writer is internal/tasks' CleanUp, called by
//     internal/service after a merge record lands a delivery task with the
//     terminal as that service's own Reader resolved it, and it examines a
//     task copy only once that liveness is Dead; so Complete is reached in
//     production by a landed task whose worker is gone, and a landed task
//     whose worker still runs, or whose terminal could not be observed,
//     stays Landed with its copy.
//   - A merge record reading closed lands nothing and is not read here: the
//     run it ended is failed, and the run tier says so. The record's reason
//     is not carried into the detail.
//   - The ordering rules this package
//     states for its own kinds, "relaunched" then "dispatched" and the
//     closed detail of "isolation-refused", are owed by their writers
//     (internal/service's relaunch and its dispatch) and checked by nothing
//     here: the store appends any text, and
//     Row.Detail is the decoder a writer calls before appending, not a gate
//     at the append.
//   - Basis.EventKind is the newest event's kind as read, so a reader that
//     must tell one event-tier answer from another reads it rather than the
//     log; it says which row decided and nothing about the rows before it.
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
