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
// a run, a stage result, a checkpoint, a task, a task state, and a hold are
// authoritative: each has one owner and one row, rewritten in place. A round
// and a task event are history: appended, never revised, and no answer to any
// present-tense question.
//
// P8 says reading the last line of an event log to decide what is true now is
// always wrong. Three things here make that mistake harder to write than to
// avoid. A task's state lives in its own table with its own accessor, so
// learning it is a different call from reading its history. TaskEvent carries a
// kind and a detail and no state field, so the newest event has no state in it
// to misread. And there is no accessor that returns the newest event alone,
// because that call's shape is the defect: it reads as though it answers what
// is true now.
//
// What that does not do is stop a determined caller from inventing the same
// mistake out of TaskEvents and its own logic. The measure is a shape that
// makes the correct call the easy one, not an enforcement, and no package that
// hands back history can be more than that.
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
// # The repository URL columns are stored redacted
//
// PRD section 8 stores repository URLs with credentials removed and recovers
// the credentialed URL from the gate at run time. P14 gives credential removal
// one owner, so this package does not implement it: Open requires a
// [vcs.Redactor] and fails with ErrNoRedactor without one.
//
// What the redactor covers is two columns, repository.upstream_url and
// repository.fork_url, which are the columns PRD section 8 designates for
// repository URLs. UpsertRepository is the only accessor that reaches the
// redactor, and it runs it on both of them on the way in.
//
// Every other column holds exactly what the caller passed. A push binding, a
// pull request reference, a run's intent, a task's session reference, a hold's
// subject and detail, a stage's log path, and the round and checkpoint payloads
// are all bound verbatim, so a caller that puts a credential in one of them has
// stored a credential, and nothing in this package will notice or remove it.
// That is the caller's responsibility, and this package does not claim
// otherwise: it is not a scrubber that everything written to it passes through.
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
// the two columns it covers either: adding a per-URL opinion about what a
// credential looks like would make this package the second owner of the
// question P14 gives to one.
//
// The redactor is supplied rather than taken from internal/vcs because that
// package's own implementation is unexported today. When the redact module PRD
// section 8 names exists, it is what callers pass.
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
// makes the sequence AppendTaskEvent assigns dense with no duplicates, and the
// revisions SetTaskState and WriteCheckpoint assign strictly increasing, under
// any number of concurrent callers. The reader pool is unbounded and carries
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
// It does not interpret what it stores. A finding set, a graph state, and a
// fixer payload are opaque bytes, because internal/findings and internal/graph
// own those vocabularies and a store that also understood them would be a
// second owner of the same contract.
//
// It does not create the home directory layout, run git, or decide what a run
// or a stage is allowed to do next. It records what happened and refuses what
// it cannot record honestly.
package store
