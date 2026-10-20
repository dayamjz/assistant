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

// forgetRunRecordsMigration names the migration that lets a run's written-once
// records go with their run, for the reason sealCheckpointTableMigration is a
// constant: ForgetRepository's comments name it rather than a version another
// branch's migration may renumber.
const forgetRunRecordsMigration = "written-once run records go with their run"

// localLandingMigration names the migration that adds the task's delivery
// posture and the local landing record, for the reason
// sealCheckpointTableMigration is a constant.
const localLandingMigration = "task delivery posture and local landing records"

// promotedPostureMigration names the migration that lets promotion write a
// task's delivery posture once and refuses every other write of it, for the
// reason sealCheckpointTableMigration is a constant.
const promotedPostureMigration = "task delivery posture written once, at intake or promotion"

// invocationServedMigration names the migration that adds the run and stage
// an agent invocation served, for the reason sealCheckpointTableMigration is
// a constant.
const invocationServedMigration = "agent invocation run and stage"

// runParkMigration names the migration that records which bound parked a run,
// for the reason sealCheckpointTableMigration is a constant.
const runParkMigration = "run park record"

// promotedIntentMigration names the migration that adds the delivery intent
// a promotion records, for the reason sealCheckpointTableMigration is a
// constant.
const promotedIntentMigration = "task delivery intent recorded at promotion"

// runStandingConsentMigration names the migration that records when a run
// was put under standing consent, for the reason sealCheckpointTableMigration
// is a constant.
const runStandingConsentMigration = "run standing consent"

// wakeHandoutMigration names the migration that records a wake's handout,
// which makes delivery at least once, for the reason
// sealCheckpointTableMigration is a constant.
const wakeHandoutMigration = "wake handouts"

// runHeldMovesMigration names the migration that counts a run's moves into
// held, which is what identifies one hold of a run, for the reason
// sealCheckpointTableMigration is a constant.
const runHeldMovesMigration = "run held moves"

// taskRelayMigration names the migration that adds the record of a
// reported investigation's findings relayed, for the reason
// sealCheckpointTableMigration is a constant.
const taskRelayMigration = "task findings relayed record"

// runNoCodeHostMigration names the migration that records when a run's
// upstream has no code host, for the reason sealCheckpointTableMigration is
// a constant.
const runNoCodeHostMigration = "run no code host"

// holdResolutionNoteMigration names the migration that records free text
// beside a hold's resolution, for the reason sealCheckpointTableMigration is
// a constant.
const holdResolutionNoteMigration = "hold resolution note"

// taskAttemptRouteMigration names the migration that records which adapter
// a task attempt was routed to and why, for the reason
// sealCheckpointTableMigration is a constant.
const taskAttemptRouteMigration = "task attempt route"

// taskRoleMigration names the migration that records the role a task was
// created under.
const taskRoleMigration = "task role"

// taskAttemptModelMigration names the migration that records the model and
// effort a launch asked for, beside the route.
const taskAttemptModelMigration = "task attempt model"

// fixerAgentMigration names the migration that records which agent opened a
// run's fixer session, so a later role that names a different agent does not
// resume it.
const fixerAgentMigration = "run fixer agent"

// invocationRoleMigration names the migration that records the role, the
// model triple, and the effort an invocation was asked for.
const invocationRoleMigration = "agent invocation role"

// holdResolutionSealMigration names the migration that seals a hold's
// resolution and note once recorded, for the reason sealCheckpointTableMigration
// is a constant.
const holdResolutionSealMigration = "hold resolution seal"

// runResolutionSkipsMigration names the migration that records what a
// run's agent resolution passed over, beside the agent it resolved.
const runResolutionSkipsMigration = "run agent resolution skips"

// taskAttemptRouteSkipsMigration names the migration that records what a
// worker's route passed over, beside the adapter it chose. It is a migration
// of its own, apart from the run's, so a test that rebuilds task_attempt can
// re-apply it alone.
const taskAttemptRouteSkipsMigration = "task attempt route skips"

// taskAttemptKnowledgeMigration names the migration that records which
// revision of a repository's curated knowledge document a task attempt's
// worker was launched with, for the reason sealCheckpointTableMigration is a
// constant.
const taskAttemptKnowledgeMigration = "task attempt knowledge revision"

// notificationMigration names the migration that adds the notification
// record, one row per wake offered to the operator's notify command, for the
// reason sealCheckpointTableMigration is a constant.
const notificationMigration = "wake notification"

// subordinateDomainsMigration names the migration that adds PRD section 8's
// records for a subordinate coordinator's domain, which section 2 defines
// and decision 20 settles: the task's domain, the delegation and delegation
// attempt records, and the escalation record, for the reason
// sealCheckpointTableMigration is a constant.
const subordinateDomainsMigration = "subordinate domains"

// domainWakesMigration names the migration that adds the wake about a
// domain rather than a task, PRD section 8's queue for a subordinate
// coordinator's own kinds, with the delegation's detector markers beside
// it, for the reason sealCheckpointTableMigration is a constant.
const domainWakesMigration = "domain wakes"

// delegationAttemptHostMigration names the migration that records, beside
// a delegation attempt's session reference, the identity of the process
// hosting the session's terminal, for the reason sealCheckpointTableMigration
// is a constant.
const delegationAttemptHostMigration = "delegation attempt host"

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
			// acknowledged, and no row is deleted, so closed rows are kept.
			// A drain acknowledged what it took until migration 31 made it a
			// handout instead.
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
			// is the closed set acknowledger.go names. AcknowledgeWakes is the
			// only accessor that writes either column; DrainWakes wrote them
			// too until migration 31 gave a drain a handout of its own.
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
		name:    "seen mark",
		statements: []string{
			// PRD section 8's seen mark: one row per landed record a person
			// was shown and marked seen, keyed the way that record is keyed -
			// a merge record by its run, a task's cleanup record by its task -
			// with kind saying which. One per home, because the database is
			// the home's. It is what section 9's catch-up reads to decide what
			// landed since the person last looked, and nothing else reads it.
			//
			// It is a set of keys and never a time or a position in a log:
			// a record is seen when it has a row here, so a record written
			// after the read a mark was made from has no row and is shown
			// again, whatever the clocks say. marked_at is when the row was
			// written and decides nothing. No foreign key, for merge_record's
			// reason: the key is the identifier the record carries, and what
			// it names is that record's table's business; MarkSeen checks in
			// its own transaction that it names a landed record.
			`CREATE TABLE seen_mark (
				kind      TEXT NOT NULL,
				key       TEXT NOT NULL,
				marked_at TEXT NOT NULL,
				PRIMARY KEY (kind, key)
			) STRICT`,
			// Appended means appended, on merge_record's terms: a row is
			// written once and neither rewritten nor removed. Like the other
			// seals, this does not guard against a later migration that drops
			// a trigger.
			`CREATE TRIGGER seen_mark_is_written_once
				BEFORE UPDATE ON seen_mark
				BEGIN
					SELECT RAISE(ABORT, 'store: a seen mark is written once by the mark that made it and is not updated');
				END`,
			`CREATE TRIGGER seen_mark_is_not_removed
				BEFORE DELETE ON seen_mark
				BEGIN
					SELECT RAISE(ABORT, 'store: a seen mark is written once by the mark that made it and is not deleted');
				END`,
		},
	},
	{
		version: 23,
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
	{
		version: 24,
		name:    forgetRunRecordsMigration,
		statements: []string{
			// PRD section 9's eject removes a gate and its records, and
			// ForgetRepository is that removal for the store. Two of a run's
			// records were sealed against any deletion: the merge record
			// (migration 13) and the run cleanup record (migration 18). Their
			// seals exist so that no write makes a row say other than what the
			// read or the cleanup decision saw, and PRD section 8's
			// "appended" is rows added and never edited; neither the PRD nor
			// either seal says such a row outlives its run. A seal against
			// every deletion made a repository with a merged or cleaned-up
			// run one that could not be forgotten at all, and the eject that
			// met one failed on the trigger partway rather than refusing.
			//
			// So the delete seal is narrowed to "while its run stands": a
			// row is still refused deletion for as long as the run row it
			// describes is there, which is every deletion that would leave a
			// run disagreeing with its record, and it may go once the run row
			// itself is gone, which only ForgetRepository does. The update
			// seals are untouched. Like every seal here, this does not guard
			// against a later migration that changes a trigger again, which
			// is a deliberate act and visible in this list as one.
			`DROP TRIGGER merge_record_is_not_removed`,
			`CREATE TRIGGER merge_record_is_not_removed
				BEFORE DELETE ON merge_record
				WHEN EXISTS (SELECT 1 FROM run WHERE id = OLD.run)
				BEGIN
					SELECT RAISE(ABORT, 'store: a merge record is written once by the read that made it and is not deleted while its run stands');
				END`,
			`DROP TRIGGER run_cleanup_is_not_removed`,
			`CREATE TRIGGER run_cleanup_is_not_removed
				BEFORE DELETE ON run_cleanup
				WHEN EXISTS (SELECT 1 FROM run WHERE id = OLD.run)
				BEGIN
					SELECT RAISE(ABORT, 'store: a run cleanup record is not deleted while its run stands');
				END`,
			// A seen mark of a merge record is keyed by the record's run
			// (migration 22), and once the record has gone with its run the
			// mark names nothing, so its delete seal is narrowed on the same
			// terms: refused while the merge record it marks stands. A mark of
			// a task's cleanup record keeps the whole seal, because nothing
			// forgets a task.
			`DROP TRIGGER seen_mark_is_not_removed`,
			`CREATE TRIGGER seen_mark_is_not_removed
				BEFORE DELETE ON seen_mark
				WHEN OLD.kind <> 'merge' OR EXISTS (SELECT 1 FROM merge_record WHERE run = OLD.key)
				BEGIN
					SELECT RAISE(ABORT, 'store: a seen mark is written once by the mark that made it and is not deleted while the record it marks stands');
				END`,
		},
	},
	{
		version: 25,
		name:    localLandingMigration,
		statements: []string{
			// PRD section 8's task row names two different things this table
			// once spelled with one word: the launch notation, which the mode
			// column migration 1 created has always held, and the delivery
			// posture, which section 6 has recorded at intake "in a field of
			// its own". This is that field. It is nullable and without a
			// default, so a task recorded before it existed, and an
			// investigation task, which has no posture, both read back
			// unknown rather than as a fabricated gated. It is written once,
			// at creation, and nothing here revises it: the record, and not
			// a later read of the key, decides the task's path.
			`ALTER TABLE task ADD COLUMN delivery TEXT`,
			// PRD section 8's local landing record: append-only and written
			// once, by the approved fast-forward that delivers a local task -
			// the task, the commit, the local default branch it moved, the
			// branch's tip before and after, and when. The task's Landed for
			// a local task is derived from it, in the tier that reads the
			// merge record, and nothing stores that fact again. One row per
			// task, sealed on merge_record's terms.
			`CREATE TABLE local_landing (
				task        TEXT PRIMARY KEY REFERENCES task(id),
				commit_id   TEXT NOT NULL,
				branch      TEXT NOT NULL,
				before      TEXT NOT NULL,
				after       TEXT NOT NULL,
				recorded_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER local_landing_is_written_once
				BEFORE UPDATE ON local_landing
				BEGIN
					SELECT RAISE(ABORT, 'store: a local landing record is written once by the fast-forward that made it and is not updated');
				END`,
			`CREATE TRIGGER local_landing_is_not_removed
				BEFORE DELETE ON local_landing
				BEGIN
					SELECT RAISE(ABORT, 'store: a local landing record is written once by the fast-forward that made it and is not deleted');
				END`,
		},
	},
	{
		version: 26,
		name:    invocationServedMigration,
		statements: []string{
			// PRD section 8's agent invocation record gained the run and the
			// stage each call served, where it served one, which is what
			// section 9's usage view totals by. Migration 8 left them out,
			// and migration 9's comment says that history named no run;
			// these two columns are what changed that.
			//
			// Both are nullable and carry no default, so a row written
			// before them reads back unknown for both, never as a guess, and
			// so does a call that served no run or no stage. Neither
			// references run(id): the history is appended and never
			// revised, so it keeps the identifier it was handed rather than
			// a foreign key. ForgetRepository removes a run's rows with the
			// run, and nothing else removes one.
			`ALTER TABLE agent_invocation ADD COLUMN run TEXT`,
			`ALTER TABLE agent_invocation ADD COLUMN stage TEXT`,
		},
	},
	{
		version: 27,
		name:    promotedPostureMigration,
		statements: []string{
			// Migration 25's delivery column was written once by having one
			// writer, CreateTask's insert, and no statement here that
			// updates it. Promotion now records the posture too (#187), in
			// TransitionTaskShape's update, so "written once" becomes this
			// trigger rather than the absence of a second writer. An update
			// that names the column is refused unless it is the one write
			// promotion makes: the row had no posture, it is given a known
			// one, and the same statement moves the shape from
			// investigation to delivery. A posture once known is never
			// revised, a delivery task recorded without one - before
			// migration 25 or promoted before this one - is never given one
			// later, and no write clears one. An update that does not name
			// the column cannot change it, so it does not reach the trigger.
			// The shape move is the only thing the trigger ties the write
			// to; that it is promotion's statement and not another update of
			// both columns rests on TransitionTaskShape being the one writer
			// of the shape.
			`CREATE TRIGGER task_delivery_is_written_once
				BEFORE UPDATE OF delivery ON task
				WHEN OLD.delivery IS NOT NULL OR NEW.delivery IS NULL
					OR OLD.shape <> 'investigation' OR NEW.shape <> 'delivery'
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s delivery posture is written once, at intake or by the promotion that makes it a delivery task, and is not revised');
				END`,
		},
	},
	{
		version: 28,
		name:    runParkMigration,
		statements: []string{
			// Which of PRD section 5's three loop bounds parked a run, as
			// RunPark encodes it. A park ends a run's record in terminated,
			// which is also what a cancellation and a supersession leave, so
			// the status alone cannot say which ending a run had; this column
			// is what qualifies that one status, and so it sits on the run
			// row beside it rather than in a table of its own. It is written
			// by TransitionRunOnPark in the transaction that moves the run to
			// terminated, so a run never reads as parked without the status
			// or the reverse.
			//
			// It is nullable with no default, so every run written before it
			// and every run no bound parked reads back unknown. The trigger
			// makes it written once: an update naming it is refused unless
			// the row had none, is given one, and already stands terminated,
			// which TransitionRunOnPark's own status move establishes first in
			// the same transaction. No statement clears it and none revises
			// it. An update that does not name the column cannot change it,
			// so it does not reach the trigger.
			`ALTER TABLE run ADD COLUMN park TEXT`,
			`CREATE TRIGGER run_park_is_written_once
				BEFORE UPDATE OF park ON run
				WHEN OLD.park IS NOT NULL OR NEW.park IS NULL OR OLD.status <> 'terminated'
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s park is written once, with the move that ends it terminated, and is not revised');
				END`,
		},
	},
	{
		version: 29,
		name:    promotedIntentMigration,
		statements: []string{
			// PRD section 5's promotion is given what the delivery is to do,
			// and until this column the one place it went was the
			// instructions file, which the worker is told to re-read and
			// which its own user can write. This is the record of it that
			// internal/service reads instead: the delivery intent as the
			// coordinator asked for it, written by TransitionTaskShape in
			// the same update that flips the shape, so no reader sees a
			// promoted task without it or an investigation task with it.
			//
			// It is nullable with no default, so an investigation task, a
			// task intake recorded as delivery, and a task promoted before
			// this migration all read back unknown. The two triggers make it
			// promotion's alone, on migration 27's terms: an insert naming a
			// value is refused, because intake records no promotion; and an
			// update naming the column is refused unless the row had none,
			// is given text that is not blank, and moves from investigation
			// to delivery in the same statement. No statement clears it and
			// none revises it. An update that does not name the column
			// cannot change it, so it does not reach the update trigger.
			`ALTER TABLE task ADD COLUMN delivery_intent TEXT`,
			`CREATE TRIGGER task_delivery_intent_is_not_inserted
				BEFORE INSERT ON task
				WHEN NEW.delivery_intent IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s delivery intent is written by the promotion that makes it a delivery task, never at intake');
				END`,
			`CREATE TRIGGER task_delivery_intent_is_written_once
				BEFORE UPDATE OF delivery_intent ON task
				WHEN OLD.delivery_intent IS NOT NULL OR NEW.delivery_intent IS NULL
					OR trim(NEW.delivery_intent) = ''
					OR OLD.shape <> 'investigation' OR NEW.shape <> 'delivery'
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s delivery intent is written once, by the promotion that makes it a delivery task, and is not revised');
				END`,
		},
	},
	{
		version: 30,
		name:    runStandingConsentMigration,
		statements: []string{
			// When a run was put under the standing consent PRD section 9
			// describes, which is the run record's own statement that the
			// program answered some of its decisions rather than a person.
			// It sits on the run row because it qualifies the run, the way
			// park does, and every surface that composes the record carries
			// it without a second read.
			//
			// It is nullable with no default, so every run written before it
			// and every run nobody gave consent to reads back unknown. The
			// trigger makes it given once: an update naming it is refused
			// unless the row had none and is given one. No statement clears
			// it, so consent cannot be withdrawn from the record of a run
			// whose decisions it already answered, and none revises when it
			// was given. An update that does not name the column cannot
			// change it, so it does not reach the trigger.
			`ALTER TABLE run ADD COLUMN standing_consent TEXT`,
			`CREATE TRIGGER run_standing_consent_is_given_once
				BEFORE UPDATE OF standing_consent ON run
				WHEN OLD.standing_consent IS NOT NULL OR NEW.standing_consent IS NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s standing consent is given once and is neither revised nor withdrawn');
				END`,
		},
	},
	{
		version: 31,
		name:    wakeHandoutMigration,
		statements: []string{
			// PRD section 8 as the owner amended it: draining hands a row
			// out, and acknowledging closes it. Migration 15 made the
			// acknowledgement the one revision a wake row takes, and a drain
			// wrote it, so delivery was at most once. These four columns are
			// the handout a drain writes instead, which leaves the row
			// queued until AcknowledgeWakes closes it.
			//
			// handed_out_at is when the latest drain handed the row out,
			// handed_out_to who it was handed to, in the closed set
			// acknowledger.go names, and handed_out_under the attachment it
			// was handed out under, which internal/service composes and this
			// package compares and never interprets: a drain passes the
			// attachments still attached, and a row handed out under one of
			// them is not handed out again. handouts counts the drains that
			// handed the row out.
			//
			// All four are nullable with no default. A row written before
			// this migration reads back with no handout, which is true of
			// every row still queued then, because a drain before it
			// acknowledged what it took, and is unknown rather than false of
			// a row acknowledged before it. DrainWakes is the one accessor
			// that writes them. The trigger keeps a handout to a queued row
			// and to whole handouts: an update naming any of the four is
			// refused once the row is acknowledged, when it leaves any of
			// the three facts unknown, or when it does not count exactly one
			// more handout than the row had. An update that does not name
			// them, the acknowledgement's, cannot change them, so it does not
			// reach the trigger, and wake_is_acknowledged_once still makes
			// the acknowledgement the row's last revision of those two.
			`ALTER TABLE wake ADD COLUMN handed_out_at TEXT`,
			`ALTER TABLE wake ADD COLUMN handed_out_to TEXT`,
			`ALTER TABLE wake ADD COLUMN handed_out_under TEXT`,
			`ALTER TABLE wake ADD COLUMN handouts INTEGER`,
			`CREATE TRIGGER wake_is_handed_out_while_queued
				BEFORE UPDATE OF handed_out_at, handed_out_to, handed_out_under, handouts ON wake
				WHEN OLD.handled_at IS NOT NULL
					OR NEW.handed_out_at IS NULL OR NEW.handed_out_to IS NULL OR NEW.handed_out_under IS NULL
					OR NEW.handouts IS NOT coalesce(OLD.handouts, 0) + 1
				BEGIN
					SELECT RAISE(ABORT, 'store: a wake is handed out only while it is queued, and each handout is counted once');
				END`,
		},
	},
	{
		version: 32,
		name:    runHeldMovesMigration,
		statements: []string{
			// How many times a run has moved into held. PRD section 5 has a
			// run moved to held surface always, and a run can hold, be
			// answered, and hold again at a later stage, so the supervisor
			// loop needs a component that advances on every hold to key the
			// wake by. This is that component, and it is written where the
			// hold becomes durable: transitionRun increments it in the
			// statement that moves the status to held, so no run reads as
			// held without the count that names this hold, and the loop
			// reads it from the run row rather than from any history (P8).
			//
			// It is nullable with no default, so a run written before it
			// reads back unknown; the first move into held after it counts
			// from there as one. The trigger makes it count moves into held
			// and nothing else: an update naming it is refused unless the
			// same statement moves the status from something else to held
			// and the count is exactly one more than it was. An update that
			// does not name the column cannot change it, so it does not
			// reach the trigger.
			`ALTER TABLE run ADD COLUMN held_moves INTEGER`,
			`CREATE TRIGGER run_held_moves_count_moves_into_held
				BEFORE UPDATE OF held_moves ON run
				WHEN OLD.status = 'held' OR NEW.status <> 'held'
					OR NEW.held_moves IS NOT coalesce(OLD.held_moves, 0) + 1
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s held moves count its moves into held, one per move, and nothing else');
				END`,
		},
	},
	{
		version: 33,
		name:    taskRelayMigration,
		statements: []string{
			// PRD section 6's lifecycle edge from Reported to Complete is
			// taken on "findings relayed", and this is the record of it:
			// the coordinator's statement, made through the service, that
			// it relayed a reported investigation's findings. It is written
			// once, by RecordTaskRelay, in the transaction that appends the
			// FindingsRelayedEventKind entry its event column names, and
			// internal/taskstate's terminal tier resolves the task Complete
			// from the row and never from the entry (P8). One row per task,
			// sealed on local_landing's terms.
			`CREATE TABLE task_relay (
				task_id     TEXT PRIMARY KEY REFERENCES task(id),
				event       INTEGER NOT NULL,
				recorded_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER task_relay_is_written_once
				BEFORE UPDATE ON task_relay
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s findings relayed record is written once and is not updated');
				END`,
			`CREATE TRIGGER task_relay_is_not_removed
				BEFORE DELETE ON task_relay
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s findings relayed record is written once and is not deleted');
				END`,
		},
	},
	{
		version: 34,
		name:    runNoCodeHostMigration,
		statements: []string{
			// Issue #283: a run whose upstream is local has no code host, so
			// it ends pushed rather than checks-passed. That fact must live
			// on the run row so a listing that has only the record (and no
			// restored checkpoint) still phrases and outcomes correctly.
			// NULL means unknown (rows written before this column); 1 means
			// no code host; 0 means a code host was named. Written once when
			// begin settles the trichotomy.
			`ALTER TABLE run ADD COLUMN no_code_host INTEGER`,
			`CREATE TRIGGER run_no_code_host_is_written_once
				BEFORE UPDATE OF no_code_host ON run
				WHEN OLD.no_code_host IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s no_code_host flag is written once and is not updated');
				END`,
		},
	},
	{
		version: 35,
		name:    holdResolutionNoteMigration,
		statements: []string{
			// Issue #279: an answer to a hold can carry free text about what
			// was decided, recorded beside the resolution. Who decided stays
			// ResolvedBy; the note is evidence, not a second claim of who.
			// NULL means unknown (rows written before this column, or a
			// resolution that carried no note).
			`ALTER TABLE hold ADD COLUMN resolution_note TEXT`,
		},
	},
	{
		version: 36,
		name:    taskAttemptRouteMigration,
		statements: []string{
			// GAP-02: every worker selection records the adapter that won
			// and the stable reason code agents.Route returned. NULL means
			// unknown (rows written before this column, or an attempt that
			// never reached routing). Both are filled once by
			// SetAttemptRoute before the process starts.
			`ALTER TABLE task_attempt ADD COLUMN adapter TEXT`,
			`ALTER TABLE task_attempt ADD COLUMN route_reason TEXT`,
		},
	},
	{
		version: 37,
		name:    taskRoleMigration,
		statements: []string{
			// A task records the role name intake was given. NULL means the
			// task names no role, which a launch reads as the worker role,
			// and is also what a row written before this column holds. The
			// name is written once.
			`ALTER TABLE task ADD COLUMN role TEXT`,
			`CREATE TRIGGER task_role_is_written_once
				BEFORE UPDATE OF role ON task
				WHEN OLD.role IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s role is written once and is not updated');
				END`,
		},
	},
	{
		version: 38,
		name:    taskAttemptModelMigration,
		statements: []string{
			// The launch records the role, the model identifier the adapter
			// was asked to start, and the effort, beside the adapter. NULL
			// means that one was not named. They are filled once, with the
			// route.
			`ALTER TABLE task_attempt ADD COLUMN role TEXT`,
			`ALTER TABLE task_attempt ADD COLUMN model TEXT`,
			`ALTER TABLE task_attempt ADD COLUMN effort TEXT`,
		},
	},
	{
		version: 39,
		name:    fixerAgentMigration,
		statements: []string{
			// The agent that opened the fixer session. NULL means unknown:
			// a row written before this column, whose session belongs to the
			// agent the run resolved, when that name is known. A later open
			// resumes the session only when this names the agent that would
			// open it.
			`ALTER TABLE run ADD COLUMN fixer_agent TEXT`,
		},
	},
	{
		version: 40,
		name:    invocationRoleMigration,
		statements: []string{
			// The role the launch ran under, the model triple it was asked
			// for, and the effort. NULL means unknown: a row written before
			// the column. An empty string means the launch did not name that
			// one, which a usage total keeps apart from a named value and
			// from unknown.
			`ALTER TABLE agent_invocation ADD COLUMN role TEXT`,
			`ALTER TABLE agent_invocation ADD COLUMN provider TEXT`,
			`ALTER TABLE agent_invocation ADD COLUMN model_type TEXT`,
			`ALTER TABLE agent_invocation ADD COLUMN version TEXT`,
			`ALTER TABLE agent_invocation ADD COLUMN effort TEXT`,
		},
	},
	{
		version: 41,
		name:    "task worker fallback selections",
		statements: []string{
			`CREATE TABLE task_agent_fallback (task_id TEXT PRIMARY KEY REFERENCES task(id), primary_agent TEXT NOT NULL, backup_agent TEXT NOT NULL, reason TEXT NOT NULL CHECK(reason IN ('quota','unavailable')), attempt INTEGER NOT NULL CHECK(attempt>0))`,
			`CREATE TRIGGER task_fallback_no_update BEFORE UPDATE ON task_agent_fallback BEGIN SELECT RAISE(ABORT,'task fallback is sealed'); END`,
			`CREATE TRIGGER task_fallback_no_delete BEFORE DELETE ON task_agent_fallback BEGIN SELECT RAISE(ABORT,'task fallback is sealed'); END`,
		},
	},
	{
		version: 42,
		name:    holdResolutionSealMigration,
		statements: []string{
			// AGE-330: a hold's resolution and the note beside it are recorded
			// once. The accessors refuse a second answer; this holds the record
			// at the database against a direct rewrite. Open redacts any
			// existing resolution_note through the supplied redactor in the
			// same transaction, before this trigger is installed.
			`CREATE TRIGGER hold_resolution_is_recorded_once
				BEFORE UPDATE OF resolution, resolved_at, resolved_by, resolution_note ON hold
				WHEN OLD.resolution IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a hold''s resolution is recorded once and is not rewritten');
				END`,
		},
	},
	{
		version: 43,
		name:    runResolutionSkipsMigration,
		statements: []string{
			// AGE-300: a selection records what it passed over beside what
			// it chose. This is the run's: the entries the service's agent
			// resolution passed over, in order, as a JSON list of
			// SkippedAgent, written with resolved_agent by
			// SetRunAgentResolution. NULL means unknown: a row written
			// before the column, or a run refused before it resolved. The
			// empty list is a resolution that passed over nothing.
			`ALTER TABLE run ADD COLUMN resolved_skipped TEXT`,
			// The resolution is recorded once. The accessor refuses a
			// second write; this holds the record at the database against a
			// direct rewrite. A row written before this migration that names
			// an agent and no skips is held too, so a later write cannot
			// attach a list to a resolution it did not see.
			`CREATE TRIGGER run_agent_resolution_is_written_once
				BEFORE UPDATE OF resolved_agent, resolved_skipped ON run
				WHEN OLD.resolved_agent IS NOT NULL OR OLD.resolved_skipped IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a run''s agent resolution is recorded once and is not rewritten');
				END`,
		},
	},
	{
		version: 44,
		name:    taskAttemptRouteSkipsMigration,
		statements: []string{
			// The same for a worker's route: the entries routing passed
			// over, written with adapter and route_reason by
			// SetAttemptRoute, on the run column's terms.
			`ALTER TABLE task_attempt ADD COLUMN route_skipped TEXT`,
			// The route is recorded once, which SetAttemptRoute already
			// refuses at the accessor; this holds all three columns at the
			// database against a direct rewrite.
			`CREATE TRIGGER task_attempt_route_is_written_once
				BEFORE UPDATE OF adapter, route_reason, route_skipped ON task_attempt
				WHEN OLD.adapter IS NOT NULL OR OLD.route_reason IS NOT NULL OR OLD.route_skipped IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: an attempt''s route is recorded once and is not rewritten');
				END`,
		},
	},
	{
		version: 45,
		name:    taskAttemptKnowledgeMigration,
		statements: []string{
			// GAP-13: the launch records which revision of the repository's
			// curated knowledge document (internal/knowledge's Document) its
			// worker's first turn carried, so what a worker was told can be
			// reproduced from the repository: the default-branch commit the
			// document was read at, and the SHA-256 of its bytes there. The
			// text itself is not stored; it is the repository's. A NULL
			// commit is unknown: a row written before these columns, or an
			// attempt that never reached the read. A known commit with a
			// NULL digest is a commit whose tree held no document. Both are
			// written once, by SetAttemptKnowledge, before the process
			// starts, and this trigger holds that against a direct rewrite.
			`ALTER TABLE task_attempt ADD COLUMN knowledge_commit TEXT`,
			`ALTER TABLE task_attempt ADD COLUMN knowledge_digest TEXT`,
			`CREATE TRIGGER task_attempt_knowledge_is_written_once
				BEFORE UPDATE OF knowledge_commit, knowledge_digest ON task_attempt
				WHEN OLD.knowledge_commit IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a task attempt''s knowledge revision is written once and is not updated');
				END`,
		},
	},
	{
		version: 46,
		name:    notificationMigration,
		statements: []string{
			// AGE-312: the record of one wake offered to the operator's
			// notify command. The row is claimed before the command runs,
			// which is what keeps a wake from being sent twice, and settled
			// once with how the command ended: outcome, the exit status when
			// it reported one, and a redacted detail. A row claimed and never
			// settled is a delivery whose end nobody recorded, a crash
			// between the two among them, and it is not offered again.
			// The claim columns never change, the settlement is the row's
			// one revision, and no row is deleted, on wake's terms.
			`CREATE TABLE notification (
				wake_id    INTEGER PRIMARY KEY REFERENCES wake(id),
				claimed_at TEXT NOT NULL,
				outcome    TEXT CHECK (outcome IN ('sent', 'failed')),
				exit_code  INTEGER,
				detail     TEXT,
				settled_at TEXT,
				CHECK ((outcome IS NULL) = (settled_at IS NULL)),
				CHECK (outcome IS NOT NULL OR (exit_code IS NULL AND detail IS NULL)),
				CHECK (outcome IS NOT 'sent' OR exit_code = 0)
			) STRICT`,
			`CREATE TRIGGER notification_claim_is_written_once
				BEFORE UPDATE OF wake_id, claimed_at ON notification
				BEGIN
					SELECT RAISE(ABORT, 'store: a notification is claimed once and the claim is not rewritten');
				END`,
			`CREATE TRIGGER notification_is_settled_once
				BEFORE UPDATE OF outcome, exit_code, detail, settled_at ON notification
				WHEN OLD.outcome IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a notification is settled once and the first outcome stands');
				END`,
			`CREATE TRIGGER notification_is_not_removed
				BEFORE DELETE ON notification
				BEGIN
					SELECT RAISE(ABORT, 'store: a notification is kept and is not deleted');
				END`,
		},
	},
	{
		version: 47,
		name:    subordinateDomainsMigration,
		statements: []string{
			// PRD section 8's task row gains its domain: written once at
			// intake from the creator's placement, kept by promotion and
			// relaunch, and never moved, because section 11 says ownership
			// never transfers. NULL is the first coordinator's own, which is
			// what every row written before this column reads as. The
			// trigger refuses every change of it, from NULL included: a
			// task is placed in a domain by the write that creates it and
			// by nothing after.
			`ALTER TABLE task ADD COLUMN domain TEXT`,
			`CREATE TRIGGER task_domain_is_written_once
				BEFORE UPDATE OF domain ON task
				WHEN NEW.domain IS NOT OLD.domain
				BEGIN
					SELECT RAISE(ABORT, 'store: a task''s domain is written at intake and never moved');
				END`,
			// The delegation record: one row per domain the coordinator
			// delegated, with the delivery posture its tasks take and the
			// anchor the write was decided against, the highest delegation
			// of the same domain the caller had seen. It is written once;
			// ended_at with its closed end_reason is its one revision, and
			// the delegation stands until that revision is written. The
			// partial index keeps one standing delegation per domain, the
			// first trigger refuses every update that is not that one
			// revision made once, and the second refuses every delete.
			`CREATE TABLE delegation (
				id         INTEGER PRIMARY KEY,
				domain     TEXT NOT NULL,
				delivery   TEXT NOT NULL,
				anchor     INTEGER NOT NULL,
				created_at TEXT NOT NULL,
				ended_at   TEXT,
				end_reason TEXT
			) STRICT`,
			`CREATE UNIQUE INDEX delegation_one_standing_per_domain ON delegation(domain) WHERE ended_at IS NULL`,
			`CREATE TRIGGER delegation_ends_once
				BEFORE UPDATE ON delegation
				WHEN OLD.ended_at IS NOT NULL
					OR NEW.ended_at IS NULL OR NEW.end_reason IS NULL
					OR NEW.id IS NOT OLD.id OR NEW.domain IS NOT OLD.domain OR NEW.delivery IS NOT OLD.delivery
					OR NEW.anchor IS NOT OLD.anchor OR NEW.created_at IS NOT OLD.created_at
				BEGIN
					SELECT RAISE(ABORT, 'store: a delegation is written once, and its end with its reason is its one revision');
				END`,
			`CREATE TRIGGER delegation_is_not_removed
				BEFORE DELETE ON delegation
				BEGIN
					SELECT RAISE(ABORT, 'store: a delegation is never deleted');
				END`,
			// One row per start of a domain's subordinate coordinator, on
			// task_attempt's terms: the record precedes the process, and
			// the session reference is filled once after it starts. A
			// relaunch is a new row anchored to the attempt history read.
			`CREATE TABLE delegation_attempt (
				delegation  INTEGER NOT NULL REFERENCES delegation(id),
				sequence    INTEGER NOT NULL,
				started_at  TEXT NOT NULL,
				session_ref TEXT,
				PRIMARY KEY (delegation, sequence)
			) STRICT`,
			`CREATE TRIGGER delegation_attempt_session_is_recorded_once
				BEFORE UPDATE ON delegation_attempt
				WHEN OLD.session_ref IS NOT NULL OR NEW.session_ref IS NULL
					OR NEW.delegation IS NOT OLD.delegation OR NEW.sequence IS NOT OLD.sequence
					OR NEW.started_at IS NOT OLD.started_at
				BEGIN
					SELECT RAISE(ABORT, 'store: a delegation attempt records its session once and nothing else');
				END`,
			`CREATE TRIGGER delegation_attempt_is_not_removed
				BEFORE DELETE ON delegation_attempt
				BEGIN
					SELECT RAISE(ABORT, 'store: a delegation attempt is never deleted');
				END`,
			// One row per decision a subordinate coordinator hands up to the
			// first coordinator, naming the task it is about and carrying
			// the text redacted, sealed on task_relay's terms. It is raised
			// as a wake of its own kind by the supervisor loop, which reads
			// this table and never the history.
			`CREATE TABLE escalation (
				id         INTEGER PRIMARY KEY,
				task_id    TEXT NOT NULL REFERENCES task(id),
				text       TEXT NOT NULL,
				created_at TEXT NOT NULL
			) STRICT`,
			`CREATE TRIGGER escalation_is_written_once
				BEFORE UPDATE ON escalation
				BEGIN
					SELECT RAISE(ABORT, 'store: an escalation is written once and is not updated');
				END`,
			`CREATE TRIGGER escalation_is_not_removed
				BEFORE DELETE ON escalation
				BEGIN
					SELECT RAISE(ABORT, 'store: an escalation is written once and is not deleted');
				END`,
		},
	},
	{
		version: 48,
		name:    domainWakesMigration,
		statements: []string{
			// A wake about a domain rather than a task: PRD section 6's
			// subordinate gone and the hold the relaunch bound raises for a
			// delegation, which the wake table cannot carry because its row
			// names a task and a subordinate coordinator is not one. The
			// row is owned by the delegation (P14): the domain is read off
			// that row and recorded nowhere here. It is the wake table's
			// shape with the task replaced by the delegation, under the
			// same seals: no column but the acknowledgement and the handout
			// may change, an acknowledgement is written once, a handout is
			// written only while the row is queued and counted once, and
			// no row is deleted. Its identifiers are a sequence of their
			// own, so a drain carries the two shapes apart.
			`CREATE TABLE domain_wake (
				id               INTEGER PRIMARY KEY,
				delegation       INTEGER NOT NULL REFERENCES delegation(id),
				kind             TEXT NOT NULL,
				marker           TEXT NOT NULL,
				created_at       TEXT NOT NULL,
				handled_at       TEXT,
				acknowledged_by  TEXT,
				handed_out_at    TEXT,
				handed_out_to    TEXT,
				handed_out_under TEXT,
				handouts         INTEGER,
				UNIQUE (delegation, kind, marker)
			) STRICT`,
			`CREATE INDEX domain_wake_unacknowledged ON domain_wake(delegation, id) WHERE handled_at IS NULL`,
			`CREATE TRIGGER domain_wake_is_appended
				BEFORE UPDATE OF id, delegation, kind, marker, created_at ON domain_wake
				BEGIN
					SELECT RAISE(ABORT, 'store: a domain wake is appended by the loop that observed it and is not rewritten');
				END`,
			`CREATE TRIGGER domain_wake_is_acknowledged_once
				BEFORE UPDATE OF handled_at, acknowledged_by ON domain_wake
				WHEN OLD.handled_at IS NOT NULL
				BEGIN
					SELECT RAISE(ABORT, 'store: a domain wake is acknowledged once and the first acknowledgement stands');
				END`,
			`CREATE TRIGGER domain_wake_is_handed_out_while_queued
				BEFORE UPDATE OF handed_out_at, handed_out_to, handed_out_under, handouts ON domain_wake
				WHEN OLD.handled_at IS NOT NULL
					OR NEW.handed_out_at IS NULL OR NEW.handed_out_to IS NULL OR NEW.handed_out_under IS NULL
					OR NEW.handouts IS NOT coalesce(OLD.handouts, 0) + 1
				BEGIN
					SELECT RAISE(ABORT, 'store: a domain wake is handed out only while it is queued, and each handout is counted once');
				END`,
			`CREATE TRIGGER domain_wake_is_not_removed
				BEFORE DELETE ON domain_wake
				BEGIN
					SELECT RAISE(ABORT, 'store: a domain wake is kept after it is drained and is not deleted');
				END`,
			// The detector markers of one delegation, on wake_marker's
			// terms: what the loop had observed of the subordinate's
			// terminal when it last passed over the delegation, rewritten
			// in place in the transaction that appends the wake they
			// stand beside. A delegation has no screen to digest and no
			// events, so the two columns are the terminal-gone ones.
			`CREATE TABLE delegation_marker (
				delegation       INTEGER PRIMARY KEY REFERENCES delegation(id),
				unobserved_since TEXT,
				gone_surfaced_at TEXT,
				updated_at       TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 49,
		name:    delegationAttemptHostMigration,
		statements: []string{
			// The identity of the process hosting the attempt's terminal,
			// a tmux server's, as internal/session's Placement.Host carries
			// it for a held session: its identifier and start time, NULL
			// for a session with no terminal. It is written in the same
			// update as session_ref, which the trigger above admits once,
			// so a restart can place a surviving subordinate's server
			// descendants as that subordinate's, exactly as the held set
			// places them; the reference alone names the pane's process.
			`ALTER TABLE delegation_attempt ADD COLUMN host_pid INTEGER`,
			`ALTER TABLE delegation_attempt ADD COLUMN host_start INTEGER`,
		},
	},
}
