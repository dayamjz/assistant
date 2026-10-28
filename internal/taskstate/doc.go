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
// PRD section 6 puts two tiers above the run, and this is the first: a
// terminal record the program wrote once. Landed is derived from the merge record of a run attributed to
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
// A task delivered under the local posture lands without a run: its Landed is
// derived from its own local landing record, store.RecordLocalLanding's row,
// which internal/service writes once in the transaction that records the
// approval whose fast-forward moved the person's local default branch, and
// it is read in this same tier, beside the merge record and on its terms.
// Complete follows from it and the cleanup record exactly as it does from a
// merge record. The detail says the branch was fast-forwarded on this
// machine and never that anything merged, because nothing was validated.
//
// A gated delivery task whose rebase leaves nothing to deliver lands too, PRD
// section 6's "nothing left to deliver": its Landed is derived from the
// nothing-to-deliver record of a run attributed to it, which
// store.TransitionRunOnNothingToDeliver writes in the transaction that ends
// the run on section 5's empty-diff short circuit, and only for a run whose
// position carries the facts internal/stages' NothingToDeliver.Lands asks
// for. It is read in this same tier, beside the merge record and on its
// terms: the newest attributed run with either record is the one named, and
// Basis.NothingToDeliver says which record it was. Complete follows from it
// and the cleanup record as from a merge record. The detail says the change
// was already on the default branch and nothing was delivered, and never
// that anything merged or was validated, because no review, test or check
// ran on it.
//
// A terminal record is final. Every run in the snapshot is read below it, so
// a run attributed later, by branch or named by the task's own row, does not
// displace it; work that continues after Landed is a new task, which is what
// internal/tasks' floor rests on when it treats Landed and Complete as
// cleared. No event kind is named landed or complete, and a worker's event by
// either name is a word with no row, which answers nothing (P8).
//
// Complete for an investigation task is resolved in this tier from its own
// record: the findings relayed record, store.RecordTaskRelay's row, which the
// coordinator writes through internal/service's coordinator-only method once
// it has relayed a reported investigation's findings. That is the
// lifecycle's Reported to Complete edge, and the record is the one thing
// that takes it: PRD section 6 does not infer Complete from the stored
// report or from the cleanup record, so a reported investigation whose copy
// was removed and whose findings nobody recorded relayed stays Reported from
// the report tier below. The record is the coordinator's word and nothing
// here witnesses the relay. It is read from the row, never from the
// "findings-relayed" entry appended beside it, which is a note (P8). Its
// writer refuses a task that is not an investigation, one with no stored
// report, and one an open hold names, inside the transaction that writes it,
// which are the decisions the edge asks to be recorded; the cleanup proof
// the edge also names is not required for Complete here, and whether the
// copy is then removed is internal/service's decision, made from the record.
// A delivery task, which a promoted investigation is, is not made Complete by
// such a record.

// # The report tier
//
// PRD section 6 puts a stored report between the run and the terminal: an
// investigation task with a stored report resolves Reported from
// SourceReport, whether its terminal is Live, Dead or Unobserved. The fact
// is internal/store's task_report row, carried on TaskSnapshot as the
// instant it was written, and never the "report-ready" history entry that
// store.StoreTaskReport appends in the same transaction: the two cannot
// disagree, and the row is the record, so the answer does not rest on a line
// of the log (P8). An entry the worker appends after its report does not
// move the task off Reported; one that claims a state of its own is listed
// in Superseded. The tier sits below the hold tier, so while a decision the
// report raised is open the task reads Held, and Reported once it is
// answered; and below the run tier, which an investigation does not reach
// in this build because it pushes nothing. A delivery task is not read by
// it, so a promoted investigation, which keeps its report row, resolves on
// the delivery tiers. Reported is not progress, so the supervisor absorbs
// nothing on it, and internal/supervisor's tables decide what it owes:
// Reported is not in flight on its state alone, and its terminal is not
// read for a terminal-gone wake.

// # The hold tier
//
// The second tier is an open hold naming the task, which resolves Held with
// SourceHold: the task is waiting on a person. It sits below the terminal
// tier, so Landed and Complete still outrank it, and above the whole run
// tier and the report tier, so a task waiting on a person never resolves
// Failed or Unknown, and does not resolve Validating, Ready or Reported
// either while the hold is open. The
// holds are internal/store's, carried on TaskSnapshot, which already reads
// every hold naming the task in the snapshot's own transaction; no record
// is added and nothing here learns what a hold's key means. Any open hold
// naming the task counts, whoever registered it: internal/tasks' StoreReport
// registers an investigation's asks, and internal/service's publish.go lists
// the sites there that write one.
// Answering a hold resolves it, and a resolved hold is not read, so the task
// falls back to whatever the tiers below say; the answer is a record, and
// no event says the task stopped waiting (P8). Basis.Hold names the hold
// that was read, so a reader comparing bases sees one open or close.
//
// # A declared wait outranks a live terminal and never a dead one
//
// PRD section 6 lists a live process signal above the event log, and PRD
// section 5 says a worker's declared external wait "silences the stall
// detector but arms a slower recheck". Read together, a worker that said it
// is waiting while its terminal runs would resolve Running, and its
// unchanged screen would be watched as a stall. The owner decided on #303
// that section 5 wins, so the process tier yields to a declared wait when,
// and only when, the probe answers Live: the log's most recent event is a
// kind whose row in kind.go is a DeclaredWait, and the task resolves Paused
// from SourceEvent with Basis.Process still Live. A dead terminal is the
// exception the decision keeps: Dead still resolves Unknown from
// SourceProcess, because a declaration is exactly the stale event a dead
// terminal must not be trusted for, and an unobserved terminal skips the
// process tier as before, so the log's Paused answers there too and the
// supervisor surfaces the unobservable terminal on its own reading. Only
// the most recent event is read, the log tier's own rule, so a worker that
// declared a wait and then said it is working again resolves Running from
// its live terminal. PRD section 6's precedence clause does not yet say
// this; the owner's wording change carries it.
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
// can state it: a Validating answer from a run the service is executing now;
// a Running answer from a terminal whose agent process is live; or a
// housekeeping pass that resolves Held from the hold tier while the service
// is executing its newest run, which has not settled (the hold tier sits
// above the run tier, so a promoted pass with an open report decision
// resolves Held while its run validates; Resolution.Progress says why). The
// two facts, whether a run is advancing and whether a terminal is live,
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
//     gone with no run and no stored report. A probe's answer is as of the probe,
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
//     stays Landed with its copy. That service ends a worker it holds once
//     the task resolves Landed and asks again when the terminal next reads
//     Dead, so a landed task keeps its copy while its worker is one that
//     service does not hold, or while the proof refuses; its landed.go
//     says so.
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
//     worker event path that reads it, internal/service's task.event, is the
//     refusal, and only for what arrives over that path: store.AppendTaskEvent
//     still records any kind for any in-process caller.
//   - The run mapping is the run's status and no finer: a failed and a
//     terminated run both read as Failed, and nothing here says why a run is in the
//     status it is in. It is pinned to
//     the run status vocabulary by a test, not reconciled with
//     internal/machine's outcome translation, which also reads a run's status
//     for a different audience.
//   - A hold naming the task decides the hold tier whether or not it also
//     names a run, which is PRD section 6's "an open hold naming this task"
//     read literally. internal/service's answer given a task closes only a
//     hold naming the task and no run, so a hold naming both would resolve
//     the task Held and not be answerable that way; no producer in this
//     build writes one.
//   - The hold tier's detail is the hold's subject, as a held run's is. For
//     an investigation's hold that is a finding's description from the
//     report the worker wrote: not an event's text, and never the log's,
//     but words a worker chose.
//   - The hold tier moves a task off Failed onto Held, and internal/tasks'
//     Clears does not clear Held, so a task held at its relaunch bound now
//     holds its branch against a queued successor until it is answered.
//     An answer does not release that successor itself: Release runs where
//     internal/service calls it, when a run finishes, at open, and once an
//     investigation's report is stored, and that
//     package's releaseQueued says the answer is not one of those sites.
//   - The log still claims Reported from a "report-ready" entry where no
//     tier above it answered, which for an investigation means an entry
//     with no report row beside it. store.StoreTaskReport is the one writer
//     of that kind in this build and writes both in one transaction, so no
//     production path reaches it; store.AppendTaskEvent would record the
//     word for any in-process caller, as the Row.Writer gap above says.
//   - A Reader loads a task's whole log for each read.
package taskstate
