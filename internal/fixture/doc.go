// Package fixture builds the adversarial subject repository the end-to-end
// harness validates against, and records what each planted condition is
// supposed to produce.
//
// It is not a realistic project and does not try to be. Every file, commit,
// and configuration value here exists so that one stage or one refusal has a
// known-correct answer, and a condition that would make the subject more
// plausible without giving some mechanism something to answer is not planted.
//
// # What this package delivers, and what it does not
//
// It delivers two things. Build constructs the scenarios on disk from nothing,
// so no git object graph is checked in and a test never depends on one nobody
// can regenerate. Conditions returns the catalog: for each planted condition,
// what was planted, which mechanism is expected to answer, and what the answer
// has to say, down to the substrings the message has to carry.
//
// It does not drive anything. Which run reaches which scenario, how a fake
// agent is pointed at a canned response, and when a stage is invoked are the
// harness's decisions, and this package records the ones it ran into as
// OpenQuestions rather than answering them. Two conditions cannot be planted
// at build time at all, because the state they need only exists partway
// through a run; those carry a Deferred plant naming the exported function the
// harness calls and the moment it has to be called at. Building the state some
// other way would be building a state the real mechanism never produces.
//
// # Why the plants go through raw git
//
// internal/vcs is the only package in this product that invokes git, and this
// package is the documented exception. It runs git directly, the way
// internal/vcs's own test helpers do, and for the same reason: a fixture built
// with the code under validation cannot show that code wrong. A subject
// repository assembled through internal/vcs would inherit whatever
// internal/vcs gets wrong, and the harness pointed at it would report agreement
// rather than correctness.
//
// The isolation that buys is the same isolation those helpers use. Every
// invocation here runs with GIT_CONFIG_GLOBAL pointing at a configuration file
// this package writes and GIT_CONFIG_NOSYSTEM set, so a developer's own git
// configuration cannot change what a scenario holds, and with the author and
// committer identity and dates stated, so two builds of one scenario differ
// only where their absolute paths differ.
//
// A scenario records how it was built rather than leaving it to be guessed:
// the resolved git binary, the home, and that configuration file are in
// Scenario.Paths, and GitInvocation hands back the pair a caller needs to run
// the same git under the same isolation. A plant applied partway through a run,
// or a harness in another process reading the manifest, would otherwise fall
// back to whatever git its own PATH resolves and whatever configuration its own
// home carries, which is the isolation lost at exactly the point it matters.
//
// # Every condition is reached by the path the mechanism sees
//
// A fixture that assembles a state directly, rather than by the route the
// product takes to it, exercises nothing on that route. This repository has
// paid for that once already, in the guard internal/safety shipped against a
// shape its own read could not produce. So the remote that advanced out of
// band advances by a push from a second clone, the copied working directory
// arrives by copying a directory, the branch that no longer diffs after a
// rebase is two histories a real rebase empties rather than an empty commit,
// and the configuration a pushed branch is not allowed to set is committed on
// that branch and pushed.
//
// # The refusals are the point
//
// A harness that only drives the happy path proves the refusals were never
// triggered, not that they work. So the refusal conditions carry the parts of
// their expectation that are easy to leave out: which sentinel the caller is
// expected to match, and, where a refusal is only useful if it tells an
// operator what to do, the action it has to name and the reason that action
// succeeds from the state the reader is in.
//
// The hostile harness installation is checked the other way round, because
// what it must produce is nothing happening. Every executable it plants writes
// a line to the scenario's tripwire file when it runs, so "none of this
// executed" is a file that must not exist rather than an absence nobody looked
// for. A tripwire that could not fire would be worse than no check at all, so
// this package's own tests run one of the planted scripts and watch the file
// appear.
package fixture
