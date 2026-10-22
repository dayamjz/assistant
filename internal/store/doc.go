// Package store is the durable record of everything the gate does. Its contract
// is PRD section 8; this comment restates the parts that are load-bearing so a
// reader of the code does not have to open the PRD to know what must stay true.
//
// The database is embedded sqlite, reached only through typed accessors. PRD
// section 8 says no caller writes SQL, and the way that is kept is that this
// package is the only one that opens the database, so a caller has no handle to
// send a statement through. That is ownership rather than enforcement, the same
// kind internal/vcs has over git: nothing stops another package from opening
// the file itself, and the answer when one needs a query is to add an accessor
// here.
//
// # Authoritative and history are different records
//
// The important part of the data model is not the field list; it is which
// records answer what is true now and which record what happened. A repository,
// a run, a stage result, a task, a hold, and a gate binding are
// authoritative: each has one owner and one row, rewritten in place. A round,
// a task event, and a task attempt are history: appended, never revised, and
// no answer to any present-tense question. A task's cleanup record is the one
// PRD section 8 calls appended and written once: one row, written with its
// proof and taking exactly one revision afterwards, the mark of when the copy
// was removed, in that order and never the reverse; TaskCleanup's doc says
// why the two are separate writes. It is what section 6's Complete is derived
// from once internal/taskstate reads it.
//
// A run's graph checkpoint history is the one record here that is appended,
// never revised, and still answers one. What P8 forbids is inferring what is
// true now from a log of things that happened, and there is nothing here to
// infer from: every entry is a whole position, complete on its own. The anchor
// is what makes the highest sequence the run's own continuation rather than
// wherever a second caller's write happened to land, and it is decided under
// the same serialization that assigns that sequence. A run's position is this
// history and nothing else.
//
// PRD section 8 says there may not be a second record holding the same
// position, and migration 1's checkpoint table was it. It has no accessor: no
// type, no read, no write, and none may be added. The table is still declared
// because a migration may not remove one, so migration 6 empties it and seals
// it with a trigger that refuses an insert instead, which is why a database
// upgraded from a build that wrote the row does not carry it forward. That
// refusal is what a caller adding new SQL for it would meet, and it is not a
// guarantee against a later migration that dropped the trigger.
//
// # A task's anchored writes
//
// Three task writes are decided against what the caller had read, on the
// terms AppendGraphCheckpoint sets for a run's position: the anchor is a
// property of the request, the decision and the write are one transaction on
// the single writer connection, and a caller whose anchor the record has
// moved past is refused with a *TaskMovedError naming where it stands rather
// than written over it (P6). AppendTaskEventAfter is anchored to the highest
// event sequence seen, BeginTaskAttempt to the highest attempt seen, and
// CreateTask to the highest Task.Row seen among the tasks of the same
// repository and branch, so an intake that read a branch, decided a task was
// independent or queued behind another, and then created it cannot be
// overtaken between the read and the write. AppendTaskEvent stays
// unanchored for a caller recording that something happened whatever else
// has happened since, and its doc says who that is.
//
// The attempt is also the attribution boundary. TaskSnapshot attributes a run
// to a task by repository and branch only from the start of the task's latest
// attempt, and attributes nothing by branch to a task never launched, which is
// what keeps a task queued behind another on the same branch from inheriting
// the blocker's runs and a relaunched task from being outranked by a run from
// before the relaunch. What the cutoff does not do is separate two tasks whose
// attempts are both live on one branch: a newer run made for one is
// attributed to both. Preventing that is the intake's disposition, which the
// anchor above protects, and not the read.
//
// The attempt's session_ref is the one record of which session an attempt
// had, filled once by SetAttemptSession after the process starts. The
// attempt's adapter, route_reason and route_skipped are the one record of
// which coding agent agents.Route selected for the worker, why, and what it
// passed over, filled once by SetAttemptRoute before the process starts.
// The run row's resolved_agent and resolved_skipped are the same record for
// the agent a run resolved, filled once by SetRunAgentResolution. The
// session_ref column migration 1 gave the task table is neither written nor
// read by this build: it stays declared because a migration may not remove a
// column, and holds NULL for every task created here.
//
// # What the terminal tier reads
//
// PRD section 6 resolves Landed and Complete from two records written once:
// the merge record, which migration 13's merge_record table holds and
// TransitionRunOnPullRequest writes in the transaction that moves the run,
// and the cleanup record, which migration 14's task_cleanup table holds and
// RecordTaskCleanup writes. TaskSnapshot carries both: the merge record of
// every attributed run, read in the snapshot's own transaction through the
// same column list and scanner Store.MergeRecord uses, so the records and the
// runs they are keyed by are one consistent read; and the task's cleanup
// record. The resolver's terminal tier, internal/taskstate, is the consumer,
// and which attributed run's merge record makes a task Landed is its
// question: the snapshot carries all of them rather than deciding.
//
// Two records beside them are written by the same cleanup decision, and
// migration 18 holds both. RunCleanup is a run's copy's cleanup record on
// TaskCleanup's terms, with one column more: a refusal, the decision's own
// word for why a copy was kept, which is the one state a row is revised out
// of and which internal/machine composes into a run's answer. TaskReport is
// PRD section 11's stored report, written once by StoreTaskReport in the
// transaction that registers the holds derived from it, so a stored report
// whose decisions were never registered cannot exist through this package;
// the parsed report is opaque bytes here, and the hold keys are the one
// fact the row holds about its contents, which is what cleanup verifies.
// TaskSnapshot carries the row's existence, as the instant it was written,
// for internal/taskstate's report tier, which resolves Reported from it.
// Migration 21's run_copy_sweep table holds the third record about a run's
// copy, RunCopySweep: what the latest sweep of a finished run's copy came to,
// from a closed set, with the occasion that asked it. It is one row per run
// rewritten by every sweep, so the row is the latest sweep and no reader
// takes it from a history (P8), and it records what a sweep found when it
// ran rather than whether anything runs in the copy now. internal/service
// writes it and internal/machine composes it into a run's answer; nothing
// decides from it, and ForgetRepository removes it with its run.
// ForgetHoldForTest is the counterfeit builder for that rule's refusal
// case, takes the test, and has no production caller. RecordTaskCleanup is
// anchored to the task's shape, read in the transaction that claims the
// record, on the terms its doc states.
//
// ForgetRepository removes a repository and every run-keyed row in one
// transaction, or refuses before removing anything. runRecords names the
// tables whose rows go with a run and refusedRunRecords the ones whose row
// refuses the removal (a task, a run process record), and a test derives the
// run-keyed tables from the catalog and holds the two lists to it, with
// seen_mark named by hand because its merge kind is keyed by the run through a
// column the catalog cannot read. The merge record, the run cleanup record
// and a merge record's seen mark go too: the migration named
// forgetRunRecordsMigration narrows their delete seals to "while what they
// describe stands", and ForgetRepository removes them after the run row.
//
// # The message record
//
// PRD section 8's message record is the message table its steering messages migration creates: one row
// per message sent to a task's worker, appended by AppendMessage before the
// send with its text passed through the redactor Open was given, and sealed
// on wake's terms. Its three facts are each written once: written or refused
// by MarkMessageWritten and MarkMessageRefused, which exclude each other,
// and acknowledged by AcknowledgeMessage, which appends the worker's
// MessageAcknowledgedEventKind event in the same transaction, so the history
// and the record cannot disagree. It is the one place delivery is read from
// (P8). Who sent a message is Sender, closed on Acknowledger's terms, with no
// member meaning a person.
//
// # The notification record
//
// The wake notification migration's notification table is one row per wake
// offered to the operator's notify command, keyed by the wake and sealed on
// wake's terms: ClaimNotification writes the claim before the command runs,
// refusing a second claim and a wake already acknowledged in the claiming
// transaction, and SettleNotification writes the outcome once, the row's
// one revision. PendingNotifications is the read that offers a wake, the
// queued ones with no row. It records how a command ended and nothing
// about the wake: the queue stays the owner of whether a wake is
// actionable, and nothing reads this table to decide one. Migration 50's
// domain_notification is the same record for the queue's second shape,
// keyed by the domain wake, with the same seals and the same accessors
// under Domain names (PendingDomainNotifications, ClaimDomainNotification,
// SettleDomainNotification), one implementation serving both tables.
//
// # The kept work record
//
// PRD section 8's kept work record is keptWorkMigration's kept_work table:
// one row per isolated copy kept because its work has not landed, written
// once by RecordKeptWork with its one owner, a task or a run, and the
// project and branch read off that owner's row in the same transaction. A
// copy holding the same commits at the same path is the same row, so the
// write is idempotent, and holds, the commits the copy's references and HEAD
// stood on, is that identity. keptWorkAgainstMigration adds the entry's
// basis, which is no part of the identity: lacks, the commits the copy
// reaches that the default branch tip against does not, or unestablished,
// the account of why that could not be computed, exactly one on every row
// written since, which the insert trigger holds; a row written before
// carries neither and reads as unestablished, which KeptWork.Established
// reports. The basis is the one thing an open unestablished entry may later
// have filled in, once, through EstablishKeptWork, and the update trigger
// holds it to that one direction and to an open row, so an answered or
// closed row is sealed whole. A discard confirmation may be written on an
// open row and withdrawn whole, which leaves it open (migration 56). Its
// one revision otherwise closes it, made once and only while
// it is open: a person's answer and its Resolver, or the
// program's KeptRemovedOnProof, which only MarkCleanupRemoved and
// MarkRunCleanupRemoved write, in the transaction that marks the owner's
// copy removed on a proof, so the program never closes an entry by
// discarding it; OpenKeptWork lists neither kind of closed row.
// MarkKeptWorkSurfaced moves when it last
// surfaced, only forward. The schema seals all three, and a row is not
// deleted while it is open or while its owner stands. ForgetRepository and
// RepositoryRemovable refuse a repository with an open entry with a
// *KeptWorkOpenError naming each. Eject's ReserveRepositoryRemoval writes
// repository_removal, naming the eject's process, in the same transaction
// as that check; CreateRun refuses a run of a reserved repository, and
// internal/cli's eject then refuses while any run copy stands on disk, so
// the gate is never removed under a copy that depends on it. No reservation
// refuses a kept work entry.
//
// An open entry also holds a new task's dispatch in its project, PRD
// section 6's "kept work is answered before new task work starts".
// keptworkhold.go is the one predicate (a task naming a project, with no
// attempt whose worker started by a recorded session, whose project has an
// open entry), read by
// KeptWorkHolding, by BeginDispatchAttempt and by ClaimAttemptLaunch
// (migration 52's launch_claimed_at, written once immediately before a
// worker's process starts), each refusing a held task with a
// *DispatchHeldError in the transaction that would write. It is derived from
// the records on every read and stored nowhere. BeginTaskAttempt, the
// relaunch claim, is not held, and a launch claim alone does not exempt a
// task: only a recorded session does.
//
// # The wake queue and the markers it is written with
//
// PRD section 8's wake is the durable wake queue: a table here and not a
// directory, migration 15's wake table, authoritative and appended. A row is
// actionable until it is acknowledged and is kept afterwards, which the
// table's triggers enforce: no column but the acknowledgement may change,
// an acknowledgement is written once, and no row is deleted. Beside it the
// wake_marker table holds PRD section 5's detector markers, one row per
// task rewritten in place: the last capture digest, the change epoch, the
// last event sequence seen, the surfaced flag, the window start, the stall
// repetition count, the unobserved-since instant and, from migration 20, the
// instant a terminal that stays unobserved last surfaced as gone. It is the
// comparison
// base for the loop's next pass and not the task's state; nothing resolves
// a state from it, and it is one row per task rather than one per detector
// because the markers advance together in the one transaction the wake is
// written in, and one row is what that transaction touches.
//
// Section 5 says an actionable wake is written in the same transaction as
// the markers it advances, and that is a shape here rather than a rule:
// AppendWakeWithMarkers is the only accessor that appends a wake and it
// advances the markers in the same transaction, so no marker can move past
// a surfaced wake that was not recorded. AdvanceWakeMarkers is the absorbed
// pass, markers and no wake, which section 5's flowchart also has. The one
// column this package derives rather than records is the change epoch,
// bumped in that transaction whenever the digest written differs from the
// one before; it is what lets a terminal kind's marker tell a screen that
// returned to an earlier value from a replay of the earlier surface.
//
// The idempotency key is the task, the kind and the marker together, as a
// uniqueness constraint. A replayed detection against a queued row is one
// wake, answered WakeAlreadyQueued; the same key against an acknowledged
// row is ErrWakeHandled, refused with the markers, because a marker that
// did not advance for a new surface is the loop's defect and not a wake to
// drop. Which component keeps a second surface distinct is the loop's to
// compose and WakeRequest's doc says what each kind's marker has to carry;
// this package checks only that the kind and the marker are non-empty, and
// that is the residual gap: a marker composed without its monotonic
// component is accepted, and the defect shows only as ErrWakeHandled after
// the first row is acknowledged. The kind is recorded as a word for the
// same reason the event kind is: what it means is internal/supervisor's
// table, this package imports neither it nor internal/taskstate, and the
// loop passes supervisor.Kind values as strings.
//
// Both writes are fenced by the supervisor loop's incarnation. Migration
// 17's supervisor_incarnation is one row, rewritten in place, holding the
// epoch of the loop incarnation that may write the queue and the markers
// now; BeginIncarnation makes the next epoch current and hands back an
// Incarnation whose field is unexported, so a value is obtained only there,
// and AppendWakeWithMarkers and AdvanceWakeMarkers refuse one that is not
// the current epoch with ErrStaleIncarnation, decided inside the transaction
// that would write. That is what lets internal/service replace a wedged
// loop with the old goroutine still running: it can unwedge and write
// nothing. The store owns which incarnation is current and the beacon file
// cites the epoch; a reader comparing the two reads this row through
// CurrentIncarnation. It is one row rather than a history because the
// question is present tense, and the epoch only grows across restarts.
//
// Draining hands a row out, and acknowledging closes it, which is PRD
// section 8 as the owner amended it. DrainWakes reads the queued rows on
// offer oldest first under a batch limit and records a handout on exactly
// those rows, who to, under which attachment and when, in the transaction
// that read them; it closes nothing, so every row it returns is still
// queued. A row is on offer when no drain has handed it out, or when the
// attachment it was last handed out under is not one the drain names as
// still attached; which attachments those are is the caller's, and
// internal/service's wakewait.go owns that rule and its residual gap. So a
// consumer that drained a row and went away before acting on it is handed
// the row again: delivery is at least once. QueuedWakes is the read that
// hands out nothing, for the program's own reconciliation and relaunch and
// a person's listing, and OfferedWakes counts what a drain would offer
// without making one. AcknowledgeWakes is what closes a row, handed out or
// not, a batch that checks every identifier and then writes every row in
// one transaction, so an unknown identifier or a row already acknowledged
// refuses the batch with a *WakeIDError and writes nothing, and a subset
// acknowledged leaves the rest queued. UnacknowledgedWakes is the queue's
// half of PRD section 5's "work is in flight": how many queued rows each
// task has, handed out or not, which internal/supervisor's in-flight table
// reads beside the resolved states, so a wake keeps its task in flight
// until it is acknowledged. Which states are in flight is that table's and
// not this package's.
//
// Who acknowledged is an Acknowledger, closed on Resolver's terms and for
// Resolver's reason: a struct with one unexported field, so no text a
// caller was handed decodes into one, and the member a surface records is
// decided in source at that surface. There is no member meaning a person,
// because no surface here witnesses one and the queue is the coordinator's.
// AcknowledgedByCoordinator's producer is internal/ipc's
// Request.WakeAcknowledger, which internal/service passes to DrainWakes as
// who the coordinator's drains hand rows to and to AcknowledgeWakes for its
// acknowledgements,
// each refused for a task's worker. AcknowledgedByProgram's one producer is
// internal/service's relaunch, for the terminal-gone rows about the session
// it replaced. This records and does not gate: every member closes a row on
// the same terms.
//
// # Subordinate domains
//
// PRD section 8's domain, delegation, delegation attempt and escalation
// records are migration 47's. A task's domain is a column of its row,
// written by CreateTask from what the caller derived of the creator's
// placement and never moved: a trigger refuses every later write of it,
// from NULL included, because section 11 has ownership never transfer, and
// NULL is the first coordinator's own, which is what every row written
// before the column reads as. delegation is written once by Delegate,
// anchored to the domain's latest delegation on CreateTask's terms, one
// standing per domain by a partial index, and ended once by EndDelegation,
// which is the row's one revision; delegation_attempt is task_attempt's
// shape for a subordinate's starts, recording with its session reference,
// in the same one write, the identity of the process hosting the session's
// terminal (migration 49's two columns), so a restart can place a
// surviving subordinate's terminal server as the subordinate's;
// EndDelegation answers in its own transaction the holds its caller names,
// on DelegationEndClosure's terms; escalation is written once by
// RecordEscalation, redacted, naming the task and nothing of the domain,
// which the task's row owns. Why a delegation ended is DelegationEnd, closed
// on Acknowledger's terms: the coordinator's domain.end, or the answer to
// the hold the relaunch bound raised for it.
//
// A wake about a domain rather than a task is the queue's second shape,
// migration 48's domain_wake, because the wake table's row names a task
// and a subordinate coordinator is not one: the same columns with the task
// replaced by the delegation, which owns the row (P14), the domain read off
// the delegation's row at every read, the same seals, and identifiers of
// its own, so a drain carries the two shapes apart and an acknowledgement
// names them apart. AppendDomainWakeWithMarkers is the one append and
// advances the delegation's markers, delegation_marker, in the same
// transaction under the same incarnation fence; DrainDomainWakes,
// OfferedDomainWakes and QueuedDomainWakes are the wake accessors' terms
// over it, with one difference the route decides: a subordinate's route is
// offered none, since every kind here is about a subordinate.
// UnacknowledgedDomainWakes is the count internal/supervisor's InFlightOf
// adds to the per-task one.
//
// A wake's recipient is decided at the drain and stored on no row (P14):
// DrainWakes, OfferedWakes and QueuedWakesFor take a WakeRoute, the domain
// the drain is for and the kinds a subordinate can act on as words, and
// join the task row and the delegation table in the drain's own
// transaction, so a delegated task's actable row is offered to its
// subordinate alone while the delegation stands and every other row to the
// first coordinator. What a kind means stays internal/supervisor's; this
// package compares the words. AcknowledgedBySubordinate and
// SentBySubordinate are the two closed sets' members for a subordinate,
// carrying its domain, which internal/service alone produces from the
// placement; WakeScopes is the read an acknowledgement is held to a domain
// with.
//
// # A read-only open
//
// OpenReadOnly is for a caller that has to read the record without being the
// service that owns it: PRD section 5's stop guard and pull guard, deciding
// whether work is in flight while the service holding the home's lock may be
// gone. It applies no migration, takes no writer connection, and refuses
// every write with ErrReadOnly, which inTx answers because there is no pool
// a write could go through. It takes no redactor, because the redactor
// guards the four redacted columns on their way in and a handle that writes
// nothing never reaches them; what any read returns of those columns is the
// redacted text the writer stored. Its callers, the guards, read the queued
// wakes and the in-flight counts, neither of which carries a repository URL,
// so no read path they take through this handle returns one to a hook's
// log. What it cannot read it reports as an
// *UnreadableError matching ErrUnreadable, with the cause beneath it: a file
// missing or not a database, a schema behind this build (ErrSchemaBehind),
// ahead of it, recorded differently, or a connection without the settings
// the guarantees above rest on. That one typed result is what a caller feeds
// a reading it could not take, which for the stop guard is unknown and never
// none. A database recorded by a build that wrote no migration digests reads
// as unreadable too, because the read-only open adds no column; the service
// that writes it adds one at its next open, and the reader follows.
//
// # The gate ownership index
//
// GateBinding is the one record here that exists to answer a question another
// package cannot answer for itself. PRD section 8 files a gate under a hash of
// its working copy's path, which makes a gate findable from a path and leaves
// the reverse, which working copies are bound to a given gate, derivable from
// nothing. That reverse is what an operation asks before it adopts a gate or
// deletes one, and inferring it from a single probe of a single path is what
// this index replaces.
//
// It enumerates rather than decides. A binding says a working copy was bound
// and has not been unbound since, which is a durable pointer and not a claim
// that the working copy is still there or still names the gate. Deciding that
// is the asking package's, and what this record buys it is the list of working
// copies worth asking about.
//
// A task's state is not a record here at all. PRD section 8 lists "task state"
// as authoritative, and this package reads that as a record's worth of answer
// rather than a row: the facts a state is made of already have owners (the run
// row, the hold row, the task row, the event log), and a stored summary of
// them is a second record that goes stale unseen. internal/taskstate resolves
// the answer from them on every read, and TaskSnapshot is the one consistent
// read it takes them through. Migration 1's task_state table is still
// declared, because a migration may not remove one, and migration 10 empties
// it and seals it with a trigger on the same terms as the checkpoint table
// above: it has no accessor and none may be added.
//
// P8 says reading the last line of an event log to decide what is true now is
// always wrong. Three things here make that mistake harder to write than to
// avoid. Learning a task's state is a different package's call from reading its
// history. TaskEvent carries a kind and a detail and no state field, so the
// newest event has no state in it to misread. And there is no accessor that
// returns the newest event alone, because that call's shape is the defect: it
// reads as though it answers what is true now.
//
// What that does not do is stop a determined caller from inventing the same
// mistake out of TaskEvents and its own logic. The measure is a shape that
// makes the correct call the easy one, not an enforcement, and no package that
// hands back history can be more than that.
//
// # Who resolved a hold
//
// PRD section 8 requires every hold resolution to record who made it, from a
// closed set, and requires that the value meaning a person decided cannot be
// produced by any path an agent reaches. ResolveHold therefore takes a Resolver
// as well as an answer, and refuses one that names nobody.
//
// The closed set is the Resolver type rather than a check the accessor runs.
// Every other vocabulary here is a defined string type, which a caller can
// build out of text it was handed; a Resolver has one unexported field and no
// conversion into it, so nothing decoded from a wire, a file, or an agent's
// output is one. What a surface may record is decided in source at that
// surface, and this package holds the set both ends of the column agree on.
//
// The value meaning a person decided has no producer in this repository, and
// that is the design rather than an omission. It belongs to a surface that
// witnessed the person, and the local protocol is not one: a person's client
// and an agent's client reach the same socket as the same user, so nothing the
// kernel attributes to a connection separates them and everything that would
// is a claim the caller writes about itself. internal/ipc answers
// ResolvedByMachineInterface for every resolution that arrives there. The
// record therefore understates who decided rather than overstating it, which
// is the direction that keeps it worth reading. One hold is served only to
// a peer the kernel reports in the foreground of its controlling terminal,
// the local landing's approval that internal/service's foreground.go holds
// to that relation; it is still recorded as the machine interface's, because
// a terminal's foreground is a fact about the peer's terminal and not a
// terminal the recording process holds, and a program that allocates a
// terminal of its own can put any command in its foreground.
//
// What keeps the person value out of a record is where a resolver may be
// written rather than anything checked at the write. ResolveHold records the
// Resolver its caller names and weighs nothing about the surface the caller
// speaks for, so a surface that derived ResolvedByMachineInterface and then
// named ResolvedByPerson in source would be recorded as it asked. No caller
// here does: the value has no producer in this repository outside the tests
// that hold this rule. The production resolutions are internal/service's: one
// recorded as ResolvedByProgram for the one hold PRD section 8 lets the
// service close itself, which that resolver's own comment names, and the
// answer to a hold a task raised, recorded as whatever internal/ipc's
// Request.HoldResolver derives for the request it arrived on, which is
// ResolvedByMachineInterface, and each answer the program gives a run's
// decision under standing consent, recorded as ResolvedByStandingConsent,
// whose own comment says why it is not ResolvedByProgram.
//
// This records and does not gate, which PRD section 8 states as a requirement
// rather than an aside. Nothing here reads a resolver to decide whether a
// resolution may proceed: every member closes a hold on the same terms, and a
// resolution that became refused because of what its record would say would be
// this column deciding something.
//
// # Schema changes are additive, and the check is against the database
//
// PRD section 8 fixes the rule: schema changes are additive, and a column added
// later reads back as unknown for old rows rather than as a fabricated zero. A
// run recorded before a field existed and a run where the field was genuinely
// zero are different facts, and a report that cannot separate them invents
// history.
//
// Optional is how that reaches a caller. The value is only reachable through
// Get, which also says whether it is there, so substituting a zero is something
// a caller writes on purpose rather than something that happens to them.
//
// The rule is enforced by verifyAdditive, and it is enforced against the
// database's own catalog after the migration's statements have run, inside the
// same transaction, rather than against the migration's text. A text check
// would pass on statements sqlite reads differently than a pattern does, which
// is the shape of a check that can succeed without checking anything. Against a
// table that already existed the migration may not drop a column, may not
// redeclare one's type, nullability, or default, and may only add columns that
// are nullable and carry no default. A table the migration creates is
// unconstrained, because it has no old rows.
//
// The scope is exactly the column shapes sqlite reports. A migration that
// rewrites the contents of existing rows passes this check, because what it
// changes is data rather than shape, and nothing here inspects data.
//
// Each migration runs in one transaction together with the row recording it, so
// there is no state in which one is half applied. A second Open on a
// fully-migrated database executes no schema statement, and a database
// interrupted partway opens and migrates again from the last migration that
// committed.
//
// Opening refuses rather than degrades in two more cases: a database carrying a
// migration this build does not have, which means an older binary is looking at
// a newer schema, and a recorded migration that differs from this build's copy,
// which means a shipped migration was edited after it had already run
// somewhere. That second comparison is over the migration's name, its statement
// count, and a digest of its statement text, so an edit made in place is caught
// and not only a renamed or resized migration. It is over the exact text, so
// reformatting a shipped migration is refused the same way rewriting one is:
// nothing here can tell them apart, and the answer to either is a new
// migration.
//
// The row that comparison cannot speak for is one recorded before digests were
// written. The digest column is nullable and is added to a schema_migration
// table that predates it, which is what lets such a database open at all; for
// those rows the digest is unknown and the comparison is name and count alone.
// Every migration this build applies records a digest, so the gap closes as
// those rows are the only ones left behind rather than growing.
//
// # Durability
//
// The database runs in WAL mode with synchronous set to NORMAL, which is a
// choice about which failures a committed transaction survives, and the two
// are not the same failure. A crash of the process holding the store does not
// lose a committed transaction: it is durable against the process going away,
// and a database reopened afterwards still carries it. An operating system
// crash or a power loss can lose the most recent commits, because NORMAL
// reports a commit without waiting for it to be forced to the disk. Neither
// failure corrupts the database; the second one costs the last writes before
// it.
//
// Process crash is the failure this product plans for, which is what recovery
// from a checkpoint is for, so NORMAL is what the stated recovery requirement
// needs. A gate that had to survive power loss without losing the last verdict
// would need synchronous FULL and would pay an fsync per commit for it; this
// package does not make that claim.
//
// # Seven columns are stored redacted
//
// PRD section 8 stores repository URLs with credentials removed and recovers
// the credentialed URL from the gate at run time. P14 gives credential removal
// one owner, so this package does not implement it: Open requires a
// [vcs.Redactor] and fails with ErrNoRedactor without one.
//
// What the redactor covers is seven columns, and six accessors reach it.
// UpsertRepository runs it on repository.upstream_url and
// repository.fork_url, the columns PRD section 8 designates for repository
// URLs. AppendMessage runs it on message.text, what was sent to a worker, and
// MarkMessageRefused runs it on message.refusal, why a send failed.
// ResolveHoldNoted and the HoldClosure paths run it on hold.resolution_note,
// free text recorded with an answer. Opening runs the same redactor over notes
// already stored when the hold resolution seal migration applies, before that
// migration seals resolved rows. SettleNotification runs it on
// notification.detail, how the operator's notify command ended.
// RecordKeptWork runs it on kept_work.detail, the cleanup decision's account
// of why a copy was kept.
//
// Every other column holds exactly what the caller passed. A push binding, a
// pull request reference, a run's intent, a promoted task's delivery intent,
// a run's fixer session reference, an attempt's session reference, a hold's
// subject and detail, a stage's log path, a task report's body and parsed
// report, and the round and graph checkpoint payloads are all bound verbatim, so a caller that puts a credential in one
// of them has stored a credential, and nothing in this package will notice
// or remove it. That is the caller's
// responsibility, and this package does not claim otherwise: it is not a
// scrubber that everything written to it passes through.
//
// Requiring a redactor leaves a gap that requiring cannot close, which is a
// redactor wired up to something inert. That failure is invisible from the
// outside: the store looks completely normal and quietly holds passwords. So
// Open runs the supplied redactor over a probe URL carrying a credential and
// refuses with ErrRedactorInert if the credential survives.
//
// What the probe establishes is exactly that the redactor is not inert. It does
// not establish that the redactor removes every credential, because one probe
// of one shape cannot, and because deciding which shapes count is the
// redactor's question rather than this package's. There is no second check on
// the columns it covers either: adding a per-URL opinion about what a
// credential looks like would make this package the second owner of the
// question P14 gives to one.
//
// The redactor is supplied rather than taken from internal/vcs because that
// package's own implementation is unexported. The redact module PRD section 8
// names is internal/redact, and that is what callers pass.
//
// # Concurrency
//
// A Store is safe for concurrent use, and the way it is safe is worth stating
// because the property callers depend on is stronger than "no data race".
//
// There are two connection pools. The writer pool holds exactly one connection
// and begins its transactions in immediate mode, so writers queue in the pool
// rather than contending for the database lock, and a read-modify-write done
// inside one transaction cannot interleave with another writer's. That is what
// makes the sequence AppendTaskEvent assigns dense with no duplicates under any
// number of concurrent callers. It is also what makes AppendGraphCheckpoint's anchor
// decision and the sequence it assigns one step rather than two, which is the
// whole of what that accessor is for, and the same step is what
// AppendTaskEventAfter, BeginTaskAttempt and CreateTask's branch anchor rest
// on. The reader pool is unbounded and carries
// queries only.
//
// The cost is that writes across the whole store serialize, including writes to
// unrelated runs. That is deliberate at this scale: one service owns one home,
// and a correctness argument a reader can check in ten lines is worth more here
// than write throughput nobody has asked for.
//
// The scope of that argument is one Store, which is the case PRD section 8
// arranges by giving one service an exclusive lock on a home. A second process
// opening the same file is outside it: those writers are serialized by the
// immediate transaction rather than by the pool, and one that waits longer than
// the busy timeout gets an error. That is a failed write a caller sees and can
// retry, not a lost one, but it is a different failure mode and this package
// does not pretend to cover it.
//
// # The settings those two sections rest on are read back
//
// Everything said above about durability and about what serializing writers
// buys is true of a database in WAL mode, with synchronous NORMAL, a busy
// timeout, and foreign keys enforced, and is not true of one running some other
// way. Those four are asked for in the connection string, and a connection
// string is a request: a store that only asked would be describing a
// configuration it never confirmed. So Open reads all four back from each pool
// after opening it, and refuses with ErrSettingNotApplied naming the setting,
// the value asked for, and the value in effect.
//
// The request and the check come from one list, requiredSettings, whose rows
// carry a setting's name, how the connection string asks for it, and what a
// connection carrying it reports. The connection string is built from those
// rows and the read-back is compared to them, so a setting cannot be asked for
// without being checked, nor checked against a value nobody asked for. Adding
// one is adding a row.
//
// What that establishes is bounded, and the bound is per setting. The journal
// mode belongs to the database file, so reading it back says which mode the
// file is in for as long as this store has it open. The busy timeout, the
// foreign key setting, and the synchronous level belong to a connection, so
// reading them back says they hold on the connection the check drew. The reader
// pool is unbounded and may open more connections later, and those are outside
// what the check speaks for.
//
// # What this package does not do
//
// It does not interpret what it stores. A finding set, a graph state, a graph
// checkpoint, and a fixer payload are opaque bytes, because internal/findings
// and internal/graph own those vocabularies and a store that also understood
// them would be a second owner of the same contract. What serializes a
// checkpoint into one of those payloads is internal/checkpoints, which is
// neither of those packages for that reason.
//
// It does not create the home directory layout, run git, or decide what a run
// or a stage is allowed to do next. It records what happened and refuses what
// it cannot record honestly.
//
// TransitionRun is where that line is easiest to misread. It refuses a move
// out of a status the caller did not expect to find the run in, which looks
// like a lifecycle rule and is not one: the set of statuses a move is legal
// out of arrives with the move, so what this package supplies is the anchored
// read-and-write those rules need and never the rules. Where they live is
// internal/runs.
//
// It does not open, resume, or reason about an agent session. Run.FixerSession
// is a reference the caller was handed, stored so a restarted service can find
// it again; what it means is the agent adapter's, and which invocations may
// carry one is internal/agents' type split.
//
// The agent invocation record PRD section 8 lists is here, as
// AppendAgentInvocation, AgentInvocations and AgentInvocationsSince, and it
// is history on that section's terms: appended, never revised, and holding
// purpose, agent, model, timing, failure category, token usage, and the run
// and stage it served, with no column a prompt, an output, a diff, or a
// credential could land in. The run and stage came later, so a row written
// before them reads both unknown, as does a call that served neither. What each vocabulary
// value means is internal/agents', and who feeds the history is
// internal/service, which is also where its one softness is owned and stated:
// the recording interface returns nothing, so a row that cannot be written is
// dropped and the history understates. The usage ceiling sums that history,
// so a dropped row is missing from the total a later start is held to.
//
// The decision-model record PRD section 5 asks for is here on the same terms,
// as AppendDecisionAnswer and DecisionAnswers: one row per answer or failed
// call, appended and never revised, with no column a question, a diff, a
// reason, or a key could land in. What the run did with that path afterwards
// is AppendDecisionFollowUp: one row per answer it follows, of a closed set of
// kinds, so an answer with no row is one nothing followed and the answer rows
// are untouched. DecisionFirings reads the two back per run of a repository,
// and decides nothing from them. Who writes the follow-ups, and the gaps in
// what they catch, is internal/service's observe.go.
//
// StageExecution is history on the same terms: one row per execution of a
// stage's body, with its start and end, written once both are known. It is
// not stage_result's duration, which is one rewritten row per stage and is
// written by nothing in this build.
//
// The run process record PRD section 11's reaping reads is here, as
// RecordProcess, ClearProcess and ProcessRecords over migration 19's
// run_process table: one row per agent process group a run's invocation
// started, naming the run, the purpose, the group, when it was recorded, and
// the leader's identity as internal/agents read it, written when the group
// is registered and deleted when its tree has been ended. It is neither
// authoritative nor history: a row says an agent was launched and its end was
// not recorded, and never that anything runs now (P8), so no accessor here
// reads it as liveness and the identity is stored as an opaque number this
// package compares with nothing. Whether the process is there is
// internal/agents' read, and what to do about it is internal/service's
// reap.go. A second record for a run and group that already has one is
// refused with ErrProcessRecorded rather than replacing it, so a launch whose
// end nobody recorded is never forgotten by a later one given the same
// number.
package store
