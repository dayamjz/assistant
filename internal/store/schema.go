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
}
