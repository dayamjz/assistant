// Package forge is the seam between this product and a code host. PRD section
// 8 gives it the provider abstraction for pull requests, checks, and merge
// state, with one interface and one adapter per provider, and PRD section 5's
// pull request and checks stages are what it answers to.
//
// Provider is the interface. GitHub is the adapter over the gh command line,
// and it is the only implementation in this module.
//
// # What "the same behavior on any forge" means here
//
// Every state a caller acts on is a typed value declared in this package, and
// a provider state this package does not recognize maps to one of them rather
// than reaching the caller as text.
//
// The claim stops there, because a wider one would be false. Two exported
// fields do carry the provider: a pull request or check URL names the host it
// lives on, and CheckRun.Reported carries the provider's own words for a state
// that was not recognized. Both are there for a person reading a report, and
// nothing enforces that a caller does not read them; what this package
// provides is that it never has to.
//
// # Zero checks is not a pass
//
// A pull request whose check list is empty has no checks registered. That is
// not the same as having checks that passed, and treating the two alike is how
// a gate reports green over a repository whose CI never ran.
//
// Evaluate turns an empty list into VerdictNoChecks, which is not green. The
// one thing that turns it into VerdictPassed is a NoCIDeclaration: the
// positive statement in configuration, PRD section 10's no_ci key, that this
// repository has no checks. The declaration then travels on the result, so the
// evidence a pass rested on stays inspectable rather than being an absence
// nobody can point at.
//
// The declaration has exactly one constructor, DeclaredNoCI, and it takes a
// resolved config.Config. Elapsed time, a repository's history, the presence
// of a workflow file, and the name of a branch cannot produce one, because
// there is no other way to build a value whose Declared reports true.
//
// What this package does not establish is where that configuration value came
// from. no_ci is TrustTrusted in internal/config's key table, so a pushed
// branch cannot set it, and reading the trusted document from the default
// branch is the gate's part of P7. Both happen before Config reaches here.
//
// A declaration decides nothing once checks exist. A repository that declares
// no_ci and then registers a check is judged on that check, and the
// declaration is absent from the result.
//
// # Settled is not the same as running
//
// A check the provider reports as cancelled has a published conclusion, and
// nothing is going to replace it. Waiting on it is waiting forever, so this
// package models the distinction rather than bucketing everything that is not
// a success together. CheckState.Settled reports whether the provider has
// published a conclusion, and CheckState.Passes reports whether a settled
// state stands in the way of a pass.
//
// CheckStateCancelled is settled and does not pass, so a run holding one
// reaches VerdictFailed and can take a fix round or report. It stays a state
// of its own rather than folding into CheckStateFailed, because "cancelled" is
// what a person needs to be told.
//
// The opposite rule applies to a state this package does not recognize. It
// becomes CheckStateUnrecognized, which is not settled, so the caller keeps
// waiting. Guessing that an unknown state is terminal risks acting on a
// verdict that has not arrived, and waiting is the error that can still be
// corrected. What bounds that wait is PRD section 10's checks_timeout, which
// belongs to the caller; this package has no clock.
//
// The zero values are the safe ones. A zero CheckState is
// CheckStateUnrecognized, a zero Verdict is VerdictNoChecks, a zero
// Mergeability is MergeabilityUnknown, and a zero PullRequestState is
// PullRequestStateUnknown, so a caller holding a value it never filled in
// concludes nothing.
//
// # Mergeability and checks are separate answers
//
// Get reports where a pull request stands and whether the provider can merge
// it. Checks reports the checks on its head. They are separate calls returning
// separate types, so "checks are green but the branch conflicts" and "checks
// are still running" are different results rather than one blended verdict.
//
// The GitHub adapter reads the provider's mergeable field and not its
// merge-state summary, because that summary folds check status into the
// mergeability answer and would collapse exactly the distinction above.
//
// # A provider that cannot answer refuses
//
// Every failure is a *Refusal naming a Reason, and a refusal is a typed result
// rather than a warning execution continues past. A provider that could not be
// run at all is ReasonUnavailable, and one that reported an authentication
// failure is ReasonUnauthenticated; neither is reported as an empty list of
// checks or a pull request that does not exist.
//
// Telling those two apart rests on what the provider reports. The GitHub
// adapter keys ReasonUnauthenticated on gh's own authentication exit status
// and does not read gh's messages looking for words, so an authentication
// failure a provider signals some other way arrives as ReasonRejected carrying
// the provider's message rather than being guessed at.
//
// # Credentials
//
// PRD section 8 gives credential removal to a redact module, which does not
// exist yet. This package writes no redactor of its own, per P14: NewGitHub
// requires a vcs.Redactor, the seam internal/vcs already declares, and every
// piece of provider text that reaches a Refusal passes through it.
//
// Two structural measures sit under that, because a redactor is a filter and a
// filter is a thing that can be handed the wrong pattern. A repository is
// addressed as owner/name, and a specifier carrying a scheme or userinfo is
// refused before gh is invoked, so this package never puts a credentialed URL
// on a command line. And the argument vector is not copied into a Refusal at
// all, so the only provider text a caller can receive is what the provider
// wrote, redacted.
//
// The residual gap is worth naming: a credential this package never sees can
// still reach gh's own output, through a credentialed remote in the
// repository's git configuration, and what removes it there is the supplied
// Redactor and nothing else. A caller that supplies one recognizing fewer
// shapes than its secrets take has that much less protection, and this package
// cannot tell.
//
// # What this package does not do
//
// It does not merge, and it has no way to. PRD section 9 keeps merge policy
// with the forge and the operator, and the gate publishes facts rather than
// reaching a verdict on them.
//
// It does not poll. Checks reports what the provider says now, and how long a
// run waits, when it re-reads, and when it gives up belong to the checks stage
// and to checks_timeout.
//
// It does not own a process tree. A provider invocation is one short-lived
// process, and a context that ends kills that process; a descendant it started
// and left behind is not pursued, which is what internal/agents does for an
// agent and what this package does not need for a command that reads and
// prints. A provider command that leaves long-lived children behind would make
// that a gap rather than a non-requirement.
//
// It does not write a pull request body. What a body says is the pull request
// stage's, generated from the round history; this package carries the text.
//
// # Requirements
//
// The gh command line on PATH, or one named with WithBinary, authenticated for
// the repository being addressed.
package forge
