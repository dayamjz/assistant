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
// Draining is a read. DrainWakes returns the queued rows oldest first under
// a batch limit and acknowledges none, so delivery is at least once by
// construction and every consumer is idempotent over a row's identifier: a
// consumer that handled a wake and did not acknowledge it, because it
// crashed or ended its turn between the two, is handed the same row again.
// AcknowledgeWakes is the one revision a row takes, a batch that checks
// every identifier and then writes every row in one transaction, so an
// unknown identifier or a row already acknowledged refuses the batch with a
// *WakeIDError and writes nothing, and a subset acknowledged leaves the rest
// queued. UnacknowledgedWakes is the queue's half of PRD section 5's "work
// is in flight": how many queued rows each task has, which
// internal/supervisor's in-flight table reads beside the resolved states.
// Which states are in flight is that table's and not this package's.
//
// Who acknowledged is an Acknowledger, closed on Resolver's terms and for
// Resolver's reason: a struct with one unexported field, so no text a
// caller was handed decodes into one, and the member a surface records is
// decided in source at that surface. There is no member meaning a person,
// because no surface here witnesses one and the queue is the coordinator's.
// AcknowledgedByCoordinator has no producer in this build: its producer is
// the two machine-interface rows that drain and acknowledge the queue, and
// those are not added before the worker set #91 produces exists, because a
// method that exists and admits a task worker to the coordinator's queue is
// worse than one that does not, so the rows wait on that set, and with them
// the derivation internal/ipc will make from the surface the way
// Request.HoldResolver does. AcknowledgedByProgram's one producer is
// internal/service's relaunch, for the terminal-gone rows about the session
// it replaced. This records and does not gate: every member closes a row on
// the same terms.
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
// is the direction that keeps it worth reading.
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
// # Four columns are stored redacted
//
// PRD section 8 stores repository URLs with credentials removed and recovers
// the credentialed URL from the gate at run time. P14 gives credential removal
// one owner, so this package does not implement it: Open requires a
// [vcs.Redactor] and fails with ErrNoRedactor without one.
//
// What the redactor covers is four columns, and three accessors reach it.
// UpsertRepository runs it on repository.upstream_url and
// repository.fork_url, the columns PRD section 8 designates for repository
// URLs. AppendMessage runs it on message.text, what was sent to a worker, and
// MarkMessageRefused runs it on message.refusal, why a send failed.
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
// dropped and the history understates.
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
