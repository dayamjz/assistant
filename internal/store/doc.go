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
// a newer schema, and a recorded migration whose name or statement count
// differs from this build's copy, which means a shipped migration was edited
// after it had already run somewhere.
//
// # Credentials do not rest here
//
// PRD section 8 stores repository URLs with credentials removed and recovers
// the credentialed URL from the gate at run time. P14 gives credential removal
// one owner, so this package does not implement it: Open requires a
// [vcs.Redactor] and fails with ErrNoRedactor without one.
//
// Requiring one leaves a gap that requiring cannot close, which is a redactor
// wired up to something inert. That failure is invisible from the outside: the
// store looks completely normal and quietly holds passwords. So Open runs the
// supplied redactor over a probe URL carrying a credential and refuses with
// ErrRedactorInert if the credential survives.
//
// What the probe establishes is exactly that the redactor is not inert. It does
// not establish that the redactor removes every credential, because one probe
// of one shape cannot, and because deciding which shapes count is the
// redactor's question rather than this package's. There is no second check at
// the write path: URLs go into the database through one function, which calls
// the redactor, and adding a per-URL opinion about what a credential looks like
// would make this package the second owner of the question P14 gives to one.
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
