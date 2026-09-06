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

			// One row per run, rewritten in place. It is not a run's
			// position; migration 5 below adds the record that is. What this
			// table is and is not for is on the Checkpoint type.
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
}
