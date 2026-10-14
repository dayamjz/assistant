package store

// migration is one step of the schema, applied in one transaction together with
// the row that records it. A migration is identified by its version, which is
// dense and starts at one, and it is never edited once it has shipped: a change
// to the schema is a new migration, because an edited one has already been
// applied on somebody's machine and will not run again there.
type migration struct {
	// version orders the migrations and is what the schema_migration table
	// records.
	version int
	// name says what the step does, and appears in a MigrationError.
	name string
	// statements are executed in order.
	statements []string
}

// sealCheckpointTableMigration names the migration that empties and seals the
// one-row checkpoint table. It is a constant so the name has one owner: a
// caller that needs to find that entry in schema looks it up by this rather
// than by a position in the list, which stops meaning the same thing the day
// the list grows.
const sealCheckpointTableMigration = "seal the one-row checkpoint"

// sealTaskStateTableMigration names the migration that empties and seals the
// task_state table, for the reason sealCheckpointTableMigration is a constant.
const sealTaskStateTableMigration = "task attribution and seal the stored task state"

// runCopySweepMigration names the migration that adds the run copy sweep
// record, for the reason sealCheckpointTableMigration is a constant.
const runCopySweepMigration = "run copy sweep records"

// schema is the migration list this package applies. Adding a column means
// appending a migration whose ALTER TABLE names a nullable column with no
// default, so rows written before it reads back unknown; migrate refuses
// anything else against a table that already exists.
var schema = []migration{
	{
		version: 1,
		name:    "baseline",
		statements: []string{
			// A repository is authoritative. Its URLs are stored with
			// credentials removed; the credentialed URL is recovered from the
			// gate at run time, so nothing here is a place a secret may rest.
			`CREATE TABLE repository (
				id             TEXT PRIMARY KEY,
				working_path   TEXT NOT NULL,
				upstream_url   TEXT NOT NULL,
				fork_url       TEXT,
				default_branch TEXT NOT NULL,
				created_at     TEXT NOT NULL,
				updated_at     TEXT NOT NULL
			) STRICT`,
			`CREATE UNIQUE INDEX repository_working_path ON repository(working_path)`,

			// A run is authoritative, and it names the exact build and
			// configuration that produced it so a surprising verdict can be
			// traced to the software that reached it.
			`CREATE TABLE run (
				id              TEXT PRIMARY KEY,
				repository_id   TEXT NOT NULL REFERENCES repository(id),
				branch          TEXT NOT NULL,
				submitted_head  TEXT NOT NULL,
				base            TEXT NOT NULL,
				current_head    TEXT,
				status          TEXT NOT NULL,
				approved_commit TEXT,
				push_binding    TEXT,
				pull_request    TEXT,
				intent          TEXT NOT NULL,
				intent_source   TEXT NOT NULL,
				build_version   TEXT NOT NULL,
				build_revision  TEXT NOT NULL,
				build_modified  INTEGER NOT NULL,
				build_go        TEXT NOT NULL,
				config_digest   TEXT NOT NULL,
				created_at      TEXT NOT NULL,
				updated_at      TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX run_repository ON run(repository_id, created_at)`,

			// A stage result is authoritative: it is the current verdict for
			// one stage of one run, and it is overwritten as the stage
			// progresses.
			`CREATE TABLE stage_result (
				run_id              TEXT NOT NULL REFERENCES run(id),
				stage               TEXT NOT NULL,
				status              TEXT NOT NULL,
				finding_count       INTEGER NOT NULL,
				duration_ns         INTEGER,
				log_path            TEXT NOT NULL,
				last_activity       TEXT,
				effective_fix_limit INTEGER,
				updated_at          TEXT NOT NULL,
				PRIMARY KEY (run_id, stage)
			) STRICT`,

			// A round is history: one row per stage execution, appended and
			// never revised. The pull request narrative is generated from it.
			`CREATE TABLE round (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id        TEXT NOT NULL REFERENCES run(id),
				stage         TEXT NOT NULL,
				number        INTEGER NOT NULL,
				findings      BLOB NOT NULL,
				selected      BLOB NOT NULL,
				selected_by   TEXT NOT NULL,
				fixer_payload BLOB NOT NULL,
				summary       TEXT NOT NULL,
				recorded_at   TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX round_run ON round(run_id, id)`,

			// One row per run, rewritten in place. It was never a run's
			// position; migration 5 below adds the record that is, and
			// migration 6 seals this one. It has no Go surface: no accessor
			// selects from it or inserts into it, and none may be added.
			//
			// The table itself stays because a migration may not remove one,
			// which verifyAdditive enforces and TestMigrationRefusesNonAdditiveChanges
			// holds. Migration 6 is the removal this schema can express.
			`CREATE TABLE checkpoint (
				run_id        TEXT PRIMARY KEY REFERENCES run(id),
				state         BLOB NOT NULL,
				position      TEXT NOT NULL,
				open_decision TEXT,
				revision      INTEGER NOT NULL,
				written_at    TEXT NOT NULL
			) STRICT`,

			`CREATE TABLE task (
				id            TEXT PRIMARY KEY,
				shape         TEXT NOT NULL,
				project       TEXT NOT NULL,
				mode          TEXT NOT NULL,
				worktree_path TEXT NOT NULL,
				session_ref   TEXT,
				run_id        TEXT REFERENCES run(id),
				pull_request  TEXT,
				created_at    TEXT NOT NULL
			) STRICT`,

			// A task's current state is authoritative and has one owner: this
			// table. It is a separate record from the event log on purpose,
			// per P8.
			`CREATE TABLE task_state (
				task_id     TEXT PRIMARY KEY REFERENCES task(id),
				state       TEXT NOT NULL,
				source      TEXT NOT NULL,
				detail      TEXT,
				revision    INTEGER NOT NULL,
				resolved_at TEXT NOT NULL
			) STRICT`,

			// A task event is history. It carries no state column, so the last
			// row cannot be read as an answer to what is true now even by a
			// caller who tries.
			`CREATE TABLE task_event (
				task_id  TEXT NOT NULL REFERENCES task(id),
				sequence INTEGER NOT NULL,
				kind     TEXT NOT NULL,
				detail   TEXT NOT NULL,
				at       TEXT NOT NULL,
				PRIMARY KEY (task_id, sequence)
			) STRICT`,

			// A hold is authoritative and keyed by a stable key, so registering
			// the same decision twice is one hold. Nothing here closes it but
			// an explicit resolution.
			`CREATE TABLE hold (
				key         TEXT PRIMARY KEY,
				run_id      TEXT REFERENCES run(id),
				task_id     TEXT REFERENCES task(id),
				subject     TEXT NOT NULL,
				detail      TEXT NOT NULL,
				opened_at   TEXT NOT NULL,
				resolution  TEXT,
				resolved_at TEXT
			) STRICT`,
			`CREATE INDEX hold_open ON hold(opened_at) WHERE resolution IS NULL`,
		},
	},
	{
		version: 2,
		name:    "gate ownership index",
		statements: []string{
			// A gate binding is authoritative: one row per working copy,
			// rewritten in place. It is what makes "which working copies are
			// bound to this gate" a question a caller asks rather than infers,
			// which a gate filed under a hash of a path cannot answer from its
			// own directory.
			//
			// The working path is the primary key because a working copy is
			// bound to at most one gate. The gate identifier is not unique:
			// two rows may name it at once while a working copy that moved has
			// not yet been unbound from its old path, and a reader that could
			// not represent that could not detect it either.
			`CREATE TABLE gate_binding (
				working_path TEXT PRIMARY KEY,
				gate_id      TEXT NOT NULL,
				bound_at     TEXT NOT NULL,
				updated_at   TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX gate_binding_gate ON gate_binding(gate_id, working_path)`,
		},
	},
	{
		version: 3,
		name:    "hold resolver",
		statements: []string{
			// PRD section 8 requires every hold resolution to record who made
			// it. The column is nullable and carries no default, so a hold
			// resolved before this migration reads back unknown rather than as
			// a resolver nobody chose. What may be written into it is the
			// closed set in resolver.go, and ResolveHold is the only accessor
			// that writes it.
			`ALTER TABLE hold ADD COLUMN resolved_by TEXT`,
		},
	},
	{
		version: 4,
		name:    "run fixer session",
		statements: []string{
			// The reference to the one durable fixer session a run keeps
			// across its fix rounds. It lives on the run because the run is
			// what it belongs to for exactly as long as the run lasts, and
			// because the alternative that suggests itself, the graph state
			// internal/graph fingerprints, is the one place it must not be:
			// a reference that changes across a resume would make that state
			// never repeat and the convergence bound never fire.
			//
			// It is nullable and carries no default, so a run recorded before
			// this column existed reads back as unknown rather than as a
			// fabricated empty session.
			`ALTER TABLE run ADD COLUMN fixer_session TEXT`,
		},
	},
	{
		version: 5,
		name:    "graph checkpoint history",
		statements: []string{
			// A run's checkpoint history: one row per position the run has
			// stood in, appended and never revised. It is what PRD section 7's
			// durability layer needs and the checkpoint table above cannot
			// give it, because two of that layer's four operations are a
			// history and a fork from a point in one.
			//
			// The columns are an index over an opaque payload. internal/graph
			// owns what a checkpoint means, so nothing here reads inside the
			// blob; run is what a read selects on and seq is what orders the
			// history and what an append's anchor is decided against. The
			// anchor itself has no column, because it is a property of the
			// request rather than of the record.
			//
			// The primary key is the pair, so two appends that computed the
			// same sequence cannot both land. That is a second line behind the
			// serialized read-and-write in AppendGraphCheckpoint rather than
			// the mechanism itself: it turns a defect in that reasoning into a
			// failed write instead of a silently duplicated position.
			//
			// run does not reference run(id), and that is deliberate rather
			// than an omission. A fork names a destination run that has no row
			// of its own and cannot be given one here, so a foreign key would
			// refuse an operation the durability contract requires to work.
			`CREATE TABLE graph_checkpoint (
				run        TEXT NOT NULL,
				seq        INTEGER NOT NULL,
				payload    BLOB NOT NULL,
				written_at TEXT NOT NULL,
				PRIMARY KEY (run, seq)
			) STRICT`,
		},
	},
	{
		version: 6,
		name:    sealCheckpointTableMigration,
		statements: []string{
			// PRD section 8 names one record for where a run stands, the
			// history migration 5 added, and says there may not be a second:
			// no row per run rewritten in place holding the same state,
			// position, and open decision beside it. The checkpoint table
			// migration 1 created is that second record, and this is where it
			// stops being one.
			//
			// The removal is a seal rather than a DROP because a migration may
			// not remove a table. That rule is PRD section 8's other schema
			// rule, verifyAdditive enforces it against the catalog, and
			// TestMigrationRefusesNonAdditiveChanges holds it, so a drop here
			// would have to weaken the guard that protects every other table's
			// rows to retire one that has none.
			//
			// What the trigger buys is that the row cannot come back by
			// accident. The Go surface that read and wrote this table is gone,
			// so a caller would have to add new SQL to write one, and this
			// makes that write fail loudly at the database rather than quietly
			// give a run a second answer to a question that has an owner. It
			// is not a guarantee against a later migration: one that dropped
			// this trigger would unseal the table, which is a deliberate act
			// and visible in the migration list as one.
			//
			// The table is emptied first, because a database migrated forward
			// from a build that wrote this record arrives here with rows in
			// it. A row here is by PRD section 8 a second answer to a question
			// the checkpoint history owns, so removing it is the removal this
			// migration is for rather than data loss; the run row it hangs off
			// is untouched. Deleting rows disturbs nothing verifyAdditive
			// checks, which compares the column catalog and never counts rows.
			`DELETE FROM checkpoint`,
			// Only INSERT is refused, and the DELETE above is what makes that
			// a complete seal rather than a partial one: with the table empty,
			// UPDATE and DELETE have nothing left to act on. Extending the
			// trigger to those two verbs instead would freeze a row written
			// before the seal in place, unremovable, which is a worse answer
			// than removing it.
			`CREATE TRIGGER checkpoint_is_not_a_record
				BEFORE INSERT ON checkpoint
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s position is the checkpoint history, and the checkpoint table is not a record');
				END`,
		},
	},
	{
		version: 7,
		name:    "run configuration rejections and resolved agent",
		statements: []string{
			// The rejection lines the run's own configuration resolution
			// reported: the keys a layer set that its origin was not allowed
			// to set, one rendered line per rejection, stored as a JSON array
			// so a resolution that dropped nothing reads back as an empty list
			// rather than as nothing recorded. It is written together with the
			// resolved digest, so the two describe one resolution.
			//
			// Both columns are nullable and carry no default, so a run
			// recorded before they existed - and a run refused before its own
			// resolution or its agent existed - reads back unknown rather
			// than as a fabricated empty answer. The record understates.
			`ALTER TABLE run ADD COLUMN config_rejections TEXT`,
			// The name of the agent the run resolved, as configuration spells
			// it. It is a name and not a second record of the agent: what the
			// name means is the agent catalog's.
			`ALTER TABLE run ADD COLUMN resolved_agent TEXT`,
		},
	},
	{
		version: 8,
		name:    "agent invocation history",
		statements: []string{
			// PRD section 8's agent invocation record: purpose, agent, model,
			// timing, failure category, and token usage. It is history, so
			// rows are appended and none is revised, and the sequence is the
			// order the store accepted them in.
			//
			// The same row fixes what is never here: there is no column a
			// prompt, an agent's output, a diff, or a credential could be
			// written into, which is how AppendAgentInvocation keeps that
			// half of the record's contract structural rather than asked of
			// its callers.
			//
			// Every usage column is nullable because a count the agent did
			// not report and a count it reported as zero are different facts,
			// and only the second may be stored as a zero. The remaining
			// columns describe the invocation itself, which the recording
			// caller always knows, so they are NOT NULL.
			`CREATE TABLE agent_invocation (
				seq                   INTEGER PRIMARY KEY,
				purpose               TEXT NOT NULL,
				agent                 TEXT NOT NULL,
				model                 TEXT NOT NULL,
				session_use           TEXT NOT NULL,
				started               TEXT NOT NULL,
				duration_ns           INTEGER NOT NULL,
				failure               TEXT NOT NULL,
				input_tokens          INTEGER,
				output_tokens         INTEGER,
				cache_read_tokens     INTEGER,
				cache_creation_tokens INTEGER,
				turns                 INTEGER
			) STRICT`,
		},
	},
	{
		version: 9,
		name:    "decision model answers",
		statements: []string{
			// PRD section 5's record of the decision-model checks: one row
			// per answer, or per call that produced none, appended and never
			// revised. It keeps which run and stage asked, which check, about
			// which path, of which pinned model version, the probability, the
			// cutoff it was compared against, why a call failed, and the
			// action of the finding the answer produced.
			//
			// The probability is nullable because a failed call has none,
			// and a zero there would read as the model having answered
			// certain-no. finding and failure are empty text rather than
			// nullable, because their absence is a known fact the recording
			// caller states: the answer produced no finding, or the call did
			// not fail.
			//
			// It is a table of its own rather than more columns on
			// agent_invocation because the two record different facts: that
			// history is a process's cost and names no run or path, and
			// every column added there for this would be NULL on every
			// agent row, reading as unknown where it is inapplicable.
			//
			// There is no column the question's text, a diff, a reason, or a
			// key could land in. The question is the check, whose text is
			// fixed in internal/judge for a given model version.
			//
			// What the run did with the path afterwards, which section 5
			// also asks this record to keep, has no column yet: nothing
			// writes it, and a column nothing writes would read as recorded
			// and empty. Adding it is a later migration.
			`CREATE TABLE decision_answer (
				seq         INTEGER PRIMARY KEY,
				run         TEXT NOT NULL,
				stage       TEXT NOT NULL,
				check_name  TEXT NOT NULL,
				path        TEXT NOT NULL,
				model       TEXT NOT NULL,
				probability REAL,
				cutoff      REAL NOT NULL,
				failure     TEXT NOT NULL,
				finding     TEXT NOT NULL,
				recorded_at TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX decision_answer_run ON decision_answer(run, seq)`,
		},
	},
	{
		version: 10,
		name:    sealTaskStateTableMigration,
		statements: []string{
			// PRD section 6 resolves a task's state deterministically from the
			// records that own the facts: the run attributed to the task's
			// branch, the process signal, and the event log. A table holding
			// that answer is the rewritten summary PRD section 8 calls the P14
			// failure, so internal/taskstate computes it on every read and
			// this table stops being one. It is sealed rather than dropped for
			// migration 6's reason: a migration may not remove a table.
			//
			// Attribution needs what a run is attributed by. Both columns are
			// nullable with no default, so a task recorded before them reads
			// back unknown: an investigation task has no branch, and the
			// record understates for an old row.
			`ALTER TABLE task ADD COLUMN repository_id TEXT`,
			`ALTER TABLE task ADD COLUMN branch TEXT`,
			`CREATE INDEX run_branch ON run(repository_id, branch, created_at)`,
			// A task_state row written by an earlier build is a second answer
			// to a question that now has none stored, so it goes, as the
			// checkpoint row did in migration 6. No code path in this
			// repository wrote one outside tests. As there, only INSERT is
			// refused: with the table empty UPDATE and DELETE have nothing to
			// act on, and extending the trigger to them would only freeze a
			// row written before the seal. Like that seal, it does not guard
			// against a later migration that drops the trigger.
			`DELETE FROM task_state`,
			`CREATE TRIGGER task_state_is_not_a_record
				BEFORE INSERT ON task_state
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s state is resolved by internal/taskstate, and the task_state table is not a record');
				END`,
		},
	},
	{
		version: 11,
		name:    "stage executions and decision answer follow-ups",
		statements: []string{
			// One row per execution of a stage's body: when it started and
			// when it ended, appended once the body has returned and never
			// revised. It is history, so a stage taking fix rounds has a row
			// per execution and none replaces another.
			//
			// It is a table of its own rather than stage_result's duration
			// column, because that row is one per stage and rewritten, so it
			// could hold the last execution and not the others; and rather
			// than round's, because a round carries the finding set, whose
			// one owner today is the run's graph state, and a writer of
			// rounds would be a second one. Both columns are NOT NULL because
			// the row is written only once both times are known: an execution
			// a hard kill interrupted has no row, and the record understates
			// rather than holding an end nobody observed.
			`CREATE TABLE stage_execution (
				seq        INTEGER PRIMARY KEY,
				run        TEXT NOT NULL,
				stage      TEXT NOT NULL,
				started_at TEXT NOT NULL,
				ended_at   TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX stage_execution_run ON stage_execution(run, seq)`,
			// What the run did next with a path a decision-model answer was
			// about: PRD section 5's "every answer is recorded with what
			// happened next", which migration 9 left without a column. One
			// row per answer it follows, so an answer with no row is one
			// nothing followed, and decision_answer itself is untouched.
			//
			// kind is the closed set store.FollowUpKind names; detail is what
			// that kind carries, as handed over: a finding's action, the
			// commit a fix round made, or the option a person chose at a
			// hold.
			`CREATE TABLE decision_followup (
				seq         INTEGER PRIMARY KEY,
				answer      INTEGER NOT NULL REFERENCES decision_answer(seq),
				kind        TEXT NOT NULL,
				stage       TEXT NOT NULL,
				detail      TEXT NOT NULL,
				recorded_at TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX decision_followup_answer ON decision_followup(answer, seq)`,
		},
	},
	{
		version: 12,
		name:    "checks wait",
		statements: []string{
			// PRD section 10's checks_timeout is an idle timeout re-armed
			// whenever the base branch advances, so what it is measured from
			// has to outlive the segment that began waiting: a restart, or a
			// caller walking away, resumes the checks stage, and a wait that
			// began again on every resume would never elapse. One row per run,
			// rewritten in place, and it is authoritative for that one fact.
			//
			// It is not a run's position. Where a run stands is its checkpoint
			// history, and nothing here says which node a run is at; it says
			// when the checks stage of a run began waiting and against which
			// base commit. The run column carries no foreign key, for the
			// decision_answer table's reason: the row is keyed by the
			// identifier the stage was handed, and what that names is
			// internal/store's run table's business rather than this one's.
			`CREATE TABLE checks_wait (
				run      TEXT PRIMARY KEY,
				base     TEXT NOT NULL,
				armed_at TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 13,
		name:    "merge record",
		statements: []string{
			// PRD section 8's merge record: append-only and written once, by
			// the code-host read that ends a run on a merge - the pull
			// request, the head read, and when. The code host owns the merge;
			// this is the program's record that it saw it. The run's passed
			// on a merge and a task's Landed are both derived from this one
			// row, and neither stores the fact again, which is why the run
			// table gains no column for it. The same row, in the closed
			// vocabulary store.PullRequestEnd, records a pull request read as
			// closed without a merge, which PRD section 5 has fail the run:
			// one read, one record, whichever end it saw.
			//
			// One row per run, written in the transaction that moves the
			// run's status, so neither exists without the other. The run
			// column carries no foreign key, for the decision_answer table's
			// reason: the row is keyed by the identifier the service holds,
			// and what that names is the run table's business.
			`CREATE TABLE merge_record (
				run              TEXT PRIMARY KEY,
				pull_request     INTEGER NOT NULL,
				head             TEXT NOT NULL,
				pull_request_end TEXT NOT NULL,
				recorded_at      TEXT NOT NULL
			) STRICT`,
			// Written once means written once. Unlike the checkpoint and
			// task_state seals, which refuse INSERT on an emptied table, this
			// table takes its rows and refuses the two verbs that would make
			// a row say something other than what the read saw. Like those
			// seals, it does not guard against a later migration that drops
			// the trigger.
			`CREATE TRIGGER merge_record_is_written_once
				BEFORE UPDATE ON merge_record
				BEGIN
					SELECT RAISE(ABORT, 'store: a merge record is written once by the read that made it and is not updated');
				END`,
			`CREATE TRIGGER merge_record_is_not_removed
				BEFORE DELETE ON merge_record
				BEGIN
					SELECT RAISE(ABORT, 'store: a merge record is written once by the read that made it and is not deleted');
				END`,
		},
	},
	{
		version: 14,
		name:    "task blockers, attempts and cleanup records",
		statements: []string{
			// The task an intake queued this one behind, when it did. PRD
			// section 6's lifecycle enters Queued when a task overlaps live
			// work, and the record of what it overlapped is what lets the
			// blocker clearing be a fact rather than a guess. It names a
			// task and nothing else: a branch has no defined resolution, and
			// a person's run on a branch is outside what this records.
			//
			// Nullable and without a default, so a task recorded before the
			// column existed, and an independent task, both read back
			// unknown rather than as a fabricated "not queued behind
			// anything".
			`ALTER TABLE task ADD COLUMN blocker_task TEXT REFERENCES task(id)`,
			// The tasks of one repository and branch, which CreateTask's
			// anchor is decided over and which an intake reads to find what
			// a new task would overlap.
			`CREATE INDEX task_branch ON task(repository_id, branch)`,

			// One row per attempt to run the task: the first launch and
			// every relaunch after it. It is history, appended and never
			// revised, except that session_ref is filled once after the
			// attempt's process starts, because a record precedes the
			// process it is about and the reference does not exist until the
			// process does.
			//
			// started_at is the attribution cutoff: a run of the task's
			// branch created at or after the latest attempt began is the
			// task's, and a task with no attempt has no branch attribution at
			// all. That is what keeps a task queued behind another on the
			// same branch from inheriting the blocker's runs, and what lets a
			// relaunch stop being outranked by a run from before it.
			//
			// The attempt's session_ref is the one record of which session an
			// attempt had. task.session_ref, from migration 1, is no longer
			// written or read: a migration may not remove a column, so it
			// stays declared and holds NULL for every task this build creates.
			`CREATE TABLE task_attempt (
				task_id     TEXT NOT NULL REFERENCES task(id),
				sequence    INTEGER NOT NULL,
				started_at  TEXT NOT NULL,
				session_ref TEXT,
				PRIMARY KEY (task_id, sequence)
			) STRICT`,

			// PRD section 8's cleanup record: written once, recording the
			// cleanup check it was made from, and what section 6's Complete
			// is derived from. proof is the closed set store.CleanupProof
			// names; examined is the commit identifiers the check read, as a
			// JSON array, so a check that examined none reads back as an
			// empty list rather than as nothing recorded. removed_at is
			// known only when the isolated copy was removed on the strength
			// of the proof: a cleanup that proved the work landed and still
			// left the copy standing is a record with a proof and no removal,
			// which is a different fact from no record.
			//
			// The task is the primary key, which is what makes the record
			// write-once at the database rather than only at the accessor.
			`CREATE TABLE task_cleanup (
				task_id     TEXT PRIMARY KEY REFERENCES task(id),
				proof       TEXT NOT NULL,
				examined    TEXT NOT NULL,
				recorded_at TEXT NOT NULL,
				removed_at  TEXT
			) STRICT`,
		},
	},
	{
		version: 15,
		name:    "durable wake queue and detector markers",
		statements: []string{
			// PRD section 8's wake: the durable wake queue, one row per
			// actionable wake, a table here and not a directory so a wake and
			// the detector markers it advances are one write. It is
			// authoritative and appended: a row is actionable until it is
			// acknowledged, and draining acknowledges a row and never deletes
			// it, so drained rows are kept.
			//
			// kind is a word the supervisor's own table gives a meaning to;
			// this package records it and decides nothing from it. marker is
			// the detector's idempotency component, composed by the loop that
			// observed the wake, and the uniqueness constraint over the task,
			// the kind and the marker is what turns a replayed detection into
			// one row rather than two. Which component keeps a legitimate
			// second surface distinct from a replay is the loop's, and
			// wake.go says what each kind's marker has to carry.
			//
			// handled_at and acknowledged_by are the one revision a row
			// takes, together and once: the acknowledgement. acknowledged_by
			// is the closed set acknowledger.go names, and AcknowledgeWakes
			// is the only accessor that writes either column.
			`CREATE TABLE wake (
				id              INTEGER PRIMARY KEY,
				task_id         TEXT NOT NULL REFERENCES task(id),
				kind            TEXT NOT NULL,
				marker          TEXT NOT NULL,
				created_at      TEXT NOT NULL,
				handled_at      TEXT,
				acknowledged_by TEXT,
				UNIQUE (task_id, kind, marker)
			) STRICT`,
			// The rows a drain and an in-flight count read: the actionable
			// ones, which are the ones not yet acknowledged.
			`CREATE INDEX wake_unacknowledged ON wake(task_id, id) WHERE handled_at IS NULL`,
			// Appended means appended. On merge_record's terms, the table
			// takes its rows and refuses the verbs that would make a row say
			// something other than what the loop recorded: no column but the
			// two acknowledgement columns may change, an acknowledged row may
			// not be acknowledged again, and no row is deleted. Like the
			// other seals, this does not guard against a later migration
			// that drops a trigger.
			`CREATE TRIGGER wake_is_appended
				BEFORE UPDATE OF id, task_id, kind, marker, created_at ON wake
				BEGIN
					SELECT RAISE(ABORT, 'store: a wake is appended by the loop that observed it and is not rewritten');
				END`,
			`CREATE TRIGGER wake_is_acknowledged_once
				BEFORE UPDATE OF handled_at, acknowledged_by ON wake
				WHEN OLD.handled_at IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a wake is acknowledged once and the first acknowledgement stands');
				END`,
			`CREATE TRIGGER wake_is_not_removed
				BEFORE DELETE ON wake
				BEGIN
					SELECT RAISE(ABORT, 'store: a wake is kept after it is drained and is not deleted');
				END`,

			// The detector markers of one task: what the supervisor loop
			// had observed when it last passed over the task, one row per
			// task, rewritten in place, and authoritative for that one fact.
			// It is not the task's state and nothing resolves a state from
			// it; it is what the next pass compares its observation against.
			//
			// change_epoch is the one column this package derives rather
			// than records: it counts the times the capture digest written
			// here differed from the one before, in the transaction that
			// writes the new digest, so a terminal kind's marker has a
			// component that advances when the screen changes and holds
			// while it does not. Everything else is what the loop handed
			// over. The nullable columns are facts the loop may not have: no
			// capture digest when the terminal could not be captured, no
			// window while nothing is waiting, no unobserved-since while the
			// terminal is observed.
			`CREATE TABLE wake_marker (
				task_id           TEXT PRIMARY KEY REFERENCES task(id),
				capture_digest    TEXT,
				change_epoch      INTEGER NOT NULL,
				event_sequence    INTEGER NOT NULL,
				surfaced          INTEGER NOT NULL,
				window_start      TEXT,
				stall_repetitions INTEGER NOT NULL,
				unobserved_since  TEXT,
				updated_at        TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 16,
		name:    "task dispatch snapshots",
		statements: []string{
			// What the task's isolated copy held when its dispatch proved it
			// isolated: every local reference and HEAD, as JSON, the set
			// PRD section 6's cleanup proof compares a copy against later.
			// It is one fact about the task's copy, rewritten by each dispatch
			// that reaches proven, so it describes the latest proven attempt.
			// It is not state: which lifecycle state the task is in is
			// resolved by internal/taskstate from other records, and nothing
			// reads this column to decide one.
			//
			// Nullable and without a default, so a task recorded before the
			// column existed, and a task whose dispatch never reached proven,
			// both read back unknown rather than as an empty copy.
			`ALTER TABLE task ADD COLUMN start_snapshot TEXT`,
			// What the person's primary checkout looked like when a dispatch
			// read it to cut the task's copy: HEAD, the branch HEAD was on if
			// any, and every local branch with its commit, as JSON. One row
			// per attempt of the task, written once and never revised, keyed
			// by the attempt it was read for. It is a separate record from
			// start_snapshot because it describes a different repository:
			// the copy is cut from the primary and is not the primary.
			//
			// The claim is only what the primary looked like at dispatch. A
			// person moves their own checkout legitimately, so a later
			// difference from this row is evidence of a change and never of
			// who made it.
			`CREATE TABLE task_primary_snapshot (
				task_id     TEXT NOT NULL,
				attempt     INTEGER NOT NULL,
				head        TEXT NOT NULL,
				head_branch TEXT,
				branches    TEXT NOT NULL,
				recorded_at TEXT NOT NULL,
				PRIMARY KEY (task_id, attempt),
				FOREIGN KEY (task_id, attempt) REFERENCES task_attempt(task_id, sequence)
			) STRICT`,
		},
	},
	{
		version: 17,
		name:    "supervisor incarnation",
		statements: []string{
			// The incarnation epoch of the supervisor loop PRD section 5
			// puts inside the service: one row, rewritten in place, holding
			// the epoch of the loop that may currently write the wake queue
			// and the markers. It is the fence #100 puts on repair in place:
			// a loop the watchdog replaced is still a goroutine nothing can
			// end, and every write it makes afterwards is decided against
			// this row in the transaction that would make it, so a replaced
			// loop that unwedges does nothing. The beacon file carries the
			// same epoch, and this row is the owner of which one is current;
			// the beacon cites it.
			//
			// It is one row rather than a history because the question it
			// answers is present tense, which one is current now, and a
			// history would be read at its tail to answer it (P8). The epoch
			// only grows, across restarts of the service too, so a beacon
			// written by an earlier process reads behind it.
			`CREATE TABLE supervisor_incarnation (
				id         INTEGER PRIMARY KEY CHECK (id = 1),
				epoch      INTEGER NOT NULL,
				started_at TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 18,
		name:    "run cleanup records and stored task reports",
		statements: []string{
			// The cleanup record of a run's isolated copy, on task_cleanup's
			// terms: PRD principle P12 covers every isolated copy, and a
			// run's copy at worktrees/<repository>/<run> is one, so the
			// decision that removes it leaves the same record a task's
			// does. It is keyed by the run and sits beside the run row
			// rather than on it, because the run row is the run's status and
			// this is what became of its copy. The run column references
			// the run table, on task_cleanup's terms rather than
			// merge_record's: the writer holds the run's own record.
			//
			// It differs from task_cleanup in one column, refusal, because
			// a run's copy is cleaned up at the moment its run settles and a
			// refusal there is what PRD section 9's status has to say about
			// a copy left standing: the word is the cleanup decision's own
			// reason, recorded here and not interpreted. A refusal is the
			// one state a row may be revised out of: it is rewritten by a
			// later refusal and replaced by a proof, and once the proof is
			// written the row takes exactly one more revision, removed_at,
			// which the triggers below hold at the database.
			`CREATE TABLE run_cleanup (
				run         TEXT PRIMARY KEY REFERENCES run(id),
				proof       TEXT,
				examined    TEXT NOT NULL,
				refusal     TEXT,
				detail      TEXT NOT NULL,
				recorded_at TEXT NOT NULL,
				removed_at  TEXT
			) STRICT`,
			`CREATE TRIGGER run_cleanup_proof_is_written_once
				BEFORE UPDATE OF proof, examined, refusal, detail, recorded_at ON run_cleanup
				WHEN OLD.proof IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a run cleanup record is written once with its proof and is not rewritten');
				END`,
			`CREATE TRIGGER run_cleanup_is_removed_once
				BEFORE UPDATE OF removed_at ON run_cleanup
				WHEN OLD.removed_at IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a run cleanup record marks its removal once and the first mark stands');
				END`,
			`CREATE TRIGGER run_cleanup_is_not_removed
				BEFORE DELETE ON run_cleanup
				BEGIN
					SELECT RAISE(ABORT, 'store: a run cleanup record is not deleted');
				END`,

			// PRD section 11's stored report: the report an investigation
			// task's worker filed, which survives the copy's cleanup. body
			// is the worker's text as it arrived, which is evidence and
			// never interpreted here; report is the same text as
			// internal/findings parsed it, encoded by the writer and opaque
			// to this package; hold_keys is the keys of the holds the
			// writer registered from it, as a JSON array, written in the
			// same transaction as the row so a stored report whose decisions
			// were never registered cannot exist through this package. It
			// is written once and never revised, and the triggers hold that
			// at the database.
			`CREATE TABLE task_report (
				task_id     TEXT PRIMARY KEY REFERENCES task(id),
				body        TEXT NOT NULL,
				report      TEXT NOT NULL,
				hold_keys   TEXT NOT NULL,
				recorded_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER task_report_is_written_once
				BEFORE UPDATE ON task_report
				BEGIN
					SELECT RAISE(ABORT, 'store: a task report is written once by the store that filed it and is not updated');
				END`,
			`CREATE TRIGGER task_report_is_not_removed
				BEFORE DELETE ON task_report
				BEGIN
					SELECT RAISE(ABORT, 'store: a task report survives cleanup and is not deleted');
				END`,
		},
	},
	{
		version: 19,
		name:    "run process records",
		statements: []string{
			// The record of an agent process a run's validation stage
			// started: which run and stage, the process group the agent
			// leads, when it was started, and the platform's PID-reuse
			// identity of the leader as internal/agents read it at launch,
			// which is what lets a later service tell the process from
			// another one given the same number. It is written when the
			// group is registered with the service's containment registry
			// and deleted when the invocation's tree has been terminated,
			// so a row that is here after a service died is exactly what
			// PRD section 11's "reap orphaned agent processes" is about.
			//
			// It is a record of what was started and never of what is
			// running now (P8): a row here says an agent was launched and
			// its end was not recorded, and whether anything of it is
			// still there is answered by looking for the process, never by
			// reading this table. identity is nullable because a platform
			// with no start-time read records none, and a row with none is
			// one recovery cannot verify and so leaves alone.
			`CREATE TABLE run_process (
				run_id     TEXT NOT NULL REFERENCES run(id),
				stage      TEXT NOT NULL,
				group_id   INTEGER NOT NULL,
				started_at TEXT NOT NULL,
				identity   TEXT,
				PRIMARY KEY (run_id, group_id)
			) STRICT`,
		},
	},
	{
		version: 20,
		name:    "terminal gone surface instant",
		statements: []string{
			// PRD section 5's crashed worker that is still silent surfaces
			// again once per escalation_window. The window between two of
			// those surfaces is measured from the last one, and this column
			// is that instant: written by the supervisor loop in the
			// transaction that appends the terminal-gone wake it records,
			// cleared when the terminal is observed again. It is a marker
			// beside unobserved_since and, like every marker, the comparison
			// base for the next pass and not the task's state. It is a
			// column rather than a read of the newest terminal-gone wake row
			// because the newest row of an appended table is not current
			// state (P8). Nullable and without a default, so a row written
			// before the column existed reads back unknown, which the loop
			// reads as a window starting at the pass that first finds it.
			`ALTER TABLE wake_marker ADD COLUMN gone_surfaced_at TEXT`,
		},
	},
	{
		version: 21,
		name:    runCopySweepMigration,
		statements: []string{
			// The record of the latest sweep of a finished run's isolated
			// copy: what it came to, from a closed set RunCopySweep's
			// accessors hold; how many processes it ended and how many
			// kept it from clearing, known exactly when the sweep listed
			// the processes; which path asked it, the move that finished
			// the run or a later service's recovery; the sweep's own
			// account as evidence; and when it was recorded. It is one
			// row per run, rewritten in place by every sweep, so the row
			// is the latest sweep and nothing derives that from a history
			// (P8). It sits beside the run row rather than on it, on
			// run_cleanup's terms: the run row is the run's status, and
			// this is what a sweep of its copy found. It records what a
			// sweep found when it ran and never whether anything runs in
			// the copy now. ForgetRepository removes it with its run.
			`CREATE TABLE run_copy_sweep (
				run       TEXT PRIMARY KEY REFERENCES run(id),
				outcome   TEXT NOT NULL,
				ended     INTEGER,
				remaining INTEGER,
				occasion  TEXT NOT NULL,
				detail    TEXT NOT NULL,
				swept_at  TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 22,
		name:    "steering messages",
		statements: []string{
			// PRD section 8's message record: one row per message sent to a
			// task's worker, naming the task, the attempt it was sent to,
			// the text with credentials removed, and who sent it, from the
			// closed set sender.go names. It is the one place whether a
			// message was delivered is read from (P8): no log line and no
			// event answers that question.
			//
			// Three facts are recorded on it, each once and never revised.
			// written_at is the provider's Send returning; refused_at with
			// refusal is Send failing, with the reason; the two are
			// exclusive, which the CHECK holds, and a row with neither is a
			// message whose send never reported, which reads as unknown and
			// is never resent. acknowledged_at with acknowledged_event is the
			// worker's acknowledgement, written by AcknowledgeMessage in the
			// transaction that appends that event to the task's history, so
			// the event and the fact on the record cannot come apart.
			`CREATE TABLE message (
				id                 INTEGER PRIMARY KEY,
				task_id            TEXT NOT NULL,
				attempt            INTEGER NOT NULL,
				text               TEXT NOT NULL,
				sent_by            TEXT NOT NULL,
				created_at         TEXT NOT NULL,
				written_at         TEXT,
				refused_at         TEXT,
				refusal            TEXT,
				acknowledged_at    TEXT,
				acknowledged_event INTEGER,
				FOREIGN KEY (task_id, attempt) REFERENCES task_attempt(task_id, sequence),
				CHECK (written_at IS NULL OR refused_at IS NULL),
				CHECK ((refused_at IS NULL) = (refusal IS NULL)),
				CHECK ((acknowledged_at IS NULL) = (acknowledged_event IS NULL))
			) STRICT`,
			// The rows the supervisor loop reads each pass: written and not
			// yet acknowledged.
			`CREATE INDEX message_unacknowledged ON message(task_id, id)
				WHERE written_at IS NOT NULL AND acknowledged_at IS NULL`,
			// Appended means appended, on wake's terms: the identifying
			// columns never change, the delivery fact is written once, the
			// acknowledgement is written once, and no row is deleted. Like
			// the other seals, this does not guard against a later
			// migration that drops a trigger.
			`CREATE TRIGGER message_is_appended
				BEFORE UPDATE OF id, task_id, attempt, text, sent_by, created_at ON message
				BEGIN
					SELECT RAISE(ABORT, 'store: a message is appended by the program that sent it and is not rewritten');
				END`,
			`CREATE TRIGGER message_delivery_is_recorded_once
				BEFORE UPDATE OF written_at, refused_at, refusal ON message
				WHEN OLD.written_at IS NOT NULL OR OLD.refused_at IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: whether a message was written or refused is recorded once and stands');
				END`,
			`CREATE TRIGGER message_is_acknowledged_once
				BEFORE UPDATE OF acknowledged_at, acknowledged_event ON message
				WHEN OLD.acknowledged_at IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a message is acknowledged once and the first acknowledgement stands');
				END`,
			`CREATE TRIGGER message_is_not_removed
				BEFORE DELETE ON message
				BEGIN
					SELECT RAISE(ABORT, 'store: a message is kept after it is delivered and is not deleted');
				END`,
		},
	},
}
