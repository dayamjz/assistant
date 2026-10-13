// Package home owns the one root everything lives under, which PRD section 8
// calls the on-disk layout: where the database, the socket, the lock, the gate
// repositories, the isolated copies, the evidence, the task records and each
// task's copy, the instance file, the beacon, the wake queue, and the logs sit
// relative to that root.
//
// It exists so that no other package spells a path. A layout every caller
// derives for itself is one every caller can derive differently, and the
// failure that produces is two components disagreeing about where a home is
// while both look correct in isolation. P14 gives the layout one owner, and
// this is it.
//
// # One root is one home
//
// The root is relocatable, and Var names the environment variable that
// relocates it. Everything below it is fixed relative to the root, so a second
// home is a second root and never a rearrangement of one.
//
// # The lock is what makes a home have exactly one service
//
// PRD section 8 gives a home exactly one service, which takes an exclusive
// operating-system lock before recovery and before binding its socket. Lock is
// that lock, and it is an advisory file lock the operating system releases when
// the holding process ends, however it ends. That property is the whole reason
// it is not a file holding a process identifier: a process identifier file
// outlives a hard kill and has to be validated against a process table that may
// have reused the number, and every validation of it is a guess. A kernel lock
// needs no validation.
//
// What it is not is a lock on the database. internal/store opens the database
// with its own connection settings and serializes its own writers, and a
// command that acts on the working copy in front of you rather than on a run
// opens that database without asking for this lock. What this lock establishes
// is that one service is serving a home, which is a different question from
// which processes may read and write its records.
//
// Two residual gaps are worth naming. An advisory lock binds the processes that
// ask for it, so a process that writes the home's files without taking it is
// not stopped by it. And the lock is per home root, so two roots pointed at one
// directory through different paths - a symbolic link, a case-insensitive
// filesystem, a bind mount - are one directory that this package would answer
// for as two homes; Open resolves symbolic links to close the first of those
// and nothing here closes the rest.
//
// # A probe of the lock is a lock taken for an instant
//
// LockState answers whether the lock is held, and it answers by taking the
// lock and letting go, because an operating-system lock offers no read-only
// query on every platform the module builds for. Its contract is the answers
// its doc comment states and nothing past them, and four gaps in it are worth
// naming here. The instant it holds the lock is visible to an Acquire with a
// wait of zero racing it, which is refused as if a service held the home. The
// same instant is visible to a second probe, so two probes overlapping make
// the second answer Held with no service running, and Held is the direction a
// wrong answer must not go in; a lone Held is therefore not evidence of a live
// service, Held counts as live only beside a beacon whose instance matches the
// instance file, and a reader with a lone Held probes once more before acting.
// The lock is per open handle, so the probe answers Held from inside the
// process that holds the lock, and a supervisor asking about itself learns
// only that its own service holds it. And Held names no holder: which process
// holds the lock is a question the instance file and the beacon exist to
// answer together, and this package only composes those paths and writes the
// first. A root that is not there answers Unknown rather than Free, so a wrong
// home path cannot read as a home with no service; Unknown otherwise comes
// mostly from a lock file the caller cannot open for writing.
//
// # A task identifier is one rule, applied before any path is composed
//
// Every path under tasks/ is composed from an identifier ValidTaskID accepted,
// and that rule is the only one: a fixed-length identifier from a closed
// alphabet, refused rather than corrected when it is anything else, with the
// names Windows reserves refused on every platform. Task, TaskCopy and
// TaskInstructions refuse what it refuses, so a traversal element or a
// separator cannot reach a path.
// The rule is applied to a string and not to a filesystem: it says an
// identifier is one path element, and nothing about whether the directory it
// names exists, is empty, or belongs to the task that claims it.
//
// # The service log is bounded, and rotation truncates in place
//
// PRD section 8 makes logs/service.log a bounded lifecycle log with rotation,
// and requires rotation to truncate in place so a process holding an open
// descriptor keeps writing to the bounded file rather than to an orphaned
// inode. Log is that: it keeps the most recent half of the bound when it
// rotates and writes on through the same descriptor.
//
// What it holds is the service's own lifecycle, not a run's output. PRD section
// 8 makes the per-stage log the authority for what a stage produced, and this
// package holds no opinion about that file beyond where it sits.
//
// # What this package does not do
//
// It does not open the database, bind the socket, or start anything. It says
// where those things go and holds the lock a service must have before it does
// them.
//
// It does not create a home as a side effect of being asked where one is. Open
// answers about a root whether or not it exists, and Create is a separate act,
// so a command that only reads cannot bring a home into existence by looking at
// it. LockState keeps the same rule: a home with no lock file is reported as
// one and not given a lock file by the asking.
//
// It does not read the instance file or write the beacon. WriteInstance is the
// one write here beyond the lock and the log, and it writes a file that is not
// state: PRD section 8 makes a published identifier no proof of readiness, and
// no reader of that file exists in this build. A service start that cannot
// write it is still refused, within the write's own bound of about two
// seconds, because a home whose instance file names a process other than the
// one holding its lock is the reading the supervisor's liveness must never be
// given. The write sweeps the temporary files an earlier write left behind in
// a crash, on the strength of the lock the caller holds; a crash in the
// window between making a temporary file and renaming it leaves one until the
// next write. The beacon's content is the supervisor's to define, and this
// package says only where it goes.
package home
