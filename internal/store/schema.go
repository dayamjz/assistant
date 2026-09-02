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

			// A checkpoint is authoritative: one row per run, rewritten after
			// every node.
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
}
