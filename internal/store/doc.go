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
// a run, a stage result, a task, a task state, a hold, and a gate binding are
// authoritative: each has one owner and one row, rewritten in place. A round
// and a task event are history: appended, never revised, and no answer to any
// present-tense question.
//
// A run's graph checkpoint history is the one record here that is appended,
// never revised, and still answers one. What P8 forbids is inferring what is
// true now from a log of things that happened, and there is nothing here to
// infer from: every entry is a whole position, complete on its own. The anchor
// is what makes the highest sequence the run's own continuation rather than
// wherever a second caller's write happened to land, and it is decided under
// the same serialization that assigns that sequence. A run's position is this
// history and nothing else, which is why Checkpoint is not in either list
// above: PRD section 8 does not name that record, nothing here writes or
// reads it outside this package's tests, and its own comment says what that
// leaves it.
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
// that hold this rule, and nothing in production resolves a hold yet.
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
// pull request reference, a run's intent, a run's fixer session reference, a
// task's session reference, a hold's subject and detail, a stage's log path,
// and the round, checkpoint, and graph checkpoint payloads are all bound
// verbatim, so a caller that puts a credential in one of them has stored a
// credential, and nothing in this package will notice or remove it. That is the
// caller's responsibility, and this package does not claim otherwise: it is not
// a scrubber that everything written to it passes through.
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
// any number of concurrent callers. It is also what makes
// AppendGraphCheckpoint's anchor decision and the sequence it assigns one step
// rather than two, which is the whole of what that accessor is for. The reader
// pool is unbounded and carries queries only.
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
// It does not yet carry every record PRD section 8 lists. The agent invocation
// record has no table and no accessor here; adding it is a later migration, and
// until then this package is not where a caller looks for one.
package store
