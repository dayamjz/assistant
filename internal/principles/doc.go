// Package principles fails the build when a principle the PRD lists has no
// test claiming it.
//
// What that proves is narrow, and the narrowness is the first thing to say
// about it. It proves that no principle in docs/prd.html is silently
// unclaimed. It does not prove that a principle holds, that a test citing one
// exercises it, or that the tests citing it are enough. A citation is a claim
// a test makes about itself; this package checks that the claim was made and
// by whom, and it has no way to check that it is true. Whatever is written on
// top of this package, in a comment, a commit message, or a line of CI
// output, may say a principle is claimed. None of it may say a principle is
// covered. Overstating a coverage check is the same defect the check exists
// to catch, and it is a defect this repository has produced more than once.
//
// The failure this is aimed at is not a test that checks the wrong thing. It
// is a principle nobody looked at, which is invisible because it looks exactly
// like a principle nobody had a problem with. Every instance of that shape
// this repository has found so far was found by a person reading, and a
// person reading does not run on every commit.
//
// # What counts as a citation
//
// A test cites a principle by calling Cite from inside its own body:
//
//	func TestOrdinaryPushToOriginIsUnaffected(t *testing.T) {
//		principles.Cite(t, principles.P1)
//		...
//	}
//
// Citations states the rule exactly and says why each half of it is there.
// The short form is that the citation is a compiled call in the test that
// makes it. A comment naming P6 is not a citation, and neither is a test named
// after one. Both were considered and both are weaker in the same way: they
// can name a principle that does not exist, they survive the deletion of every
// assertion they were written about, and a matcher for them is satisfied by
// text that no test ever runs. The call cannot name a principle that has no
// constant, has to be deleted by hand, and appears in the test's own log
// beside the failure.
//
// # Why this reads source
//
// A test whose only evidence is that source text matches a pattern proves
// nothing about behavior, and this repository rejects one. This package reads
// source deliberately and is not an exception to that rule, because what it
// measures is not behavior. It measures which claims the test suite makes,
// and a claim is a thing written in the test suite: there is no behavior
// behind it to observe instead. The rule it would break is the one where the
// reading stands in for the behavior, and nothing this package reports says a
// principle holds.
//
// # The gaps are written down, not inferred
//
// Some principles are claimed by no test here, mostly because the subsystem
// the principle governs is not built yet. unclaimed.go is that list, one row
// per principle, and Check treats a row as accounting for the gap. A row is a
// declaration that the gap is known; it is not a judgement that the gap is
// fine, and the sentence beside it is prose nobody verified. A row whose
// principle a test turns out to cite is a failure, so a declaration cannot
// outlive the gap it describes.
//
// # How it runs
//
// TestEveryPrincipleIsClaimedOrDeclaredUnclaimed is the check. It runs under
// `make check` and under CI on every platform with everything else, so a
// principle added to the PRD turns a branch red with nothing further wired.
//
// # Residual gaps
//
// A test that cites a principle and checks nothing counts. That is the
// boundary of the whole idea, not an implementation shortcut: the alternative
// is a mechanism that reads a test and decides what it establishes.
//
// The scan reads source rather than a built test binary, so a test excluded by
// a build constraint on the platform running the check, and a test whose body
// calls t.Skip, both still cite.
//
// The PRD's list is what FromPRD's rule finds. A principle the PRD introduces
// in prose without a badge in its principles section is not in the list, and
// nothing here would ask for it.
//
// Only principles are checked. They were first because they are enumerable
// from one place and load-bearing everywhere, not because they are the only
// claims in this repository that nothing counts.
//
// # Another claim class
//
// The shape here is three parts: an owner that enumerates the claims, a
// vocabulary a test speaks a claim in, and a written list of what nothing
// claims. Only the first is specific to principles. internal/outcomes is the
// second claim class, and it took the first part alone: the build already
// exports the set it declares, so it needs no vocabulary, and its comparison
// covers that set whole, so it has no counterpart to unclaimed.go. It states
// its own residual gaps instead. Nothing here is generalized for the two of
// them, deliberately: two instances are still not enough to know which parts
// are common and which are each one's, and that package's documentation says
// what the two have in common so far.
package principles
