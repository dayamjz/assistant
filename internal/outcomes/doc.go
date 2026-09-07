// Package outcomes fails the build when the PRD's outcome row and
// internal/machine's outcome set stop saying the same thing.
//
// What that proves is narrow, and the narrowness is the first thing to say
// about it. It proves that both sides declare the same values, in the same
// order, in the same two groups, and that every value the build declares
// carries a next action. It does not prove that the six are the right six,
// that an outcome means on the wire what the row's sentence about it says, or
// that any run ever reaches the outcome it should. Whatever is written on top
// of this package may say the two lists agree. None of it may say the machine
// interface is correct.
//
// The failure this is aimed at is not a wrong outcome. It is two closed sets
// stated in two places with nothing holding them together, which is invisible
// because each side reads as complete on its own. That failure has happened
// here: internal/machine translated a run still executing into failed while
// the PRD's set had no member for reading one, so attaching to an advancing
// run reported it FAILED. Repairing it took three rounds of review and a
// mechanical sweep that still missed two sites, and every part of finding it
// was a person reading. A person reading does not run on every commit.
//
// # What counts as a declaration
//
// The PRD declares an outcome by marking it inside one row of the contract
// table in its Surfaces section: the section anchored <section id="surfaces">,
// and inside it the row anchored <tr id="outcome-set">:
//
//	<code data-outcome="finished">checks-passed</code>
//	<code data-outcome="unfinished">executing</code>
//
// The element's text is the value that travels and the attribute is the group
// the row puts it in. The marker and not the word is what is read, because
// that section discusses these values in prose as well as declaring them: the
// word executing is set in a code element there more than once and declares an
// outcome once. The row and not the section is what is read for the same
// reason one step further out, and because that row says it is where the set
// is stated: a marker anywhere else in the document is a refusal rather than a
// seventh outcome, so the sentence in the row stays true of the mechanism. The
// rule is one a reader can apply by eye, and it is not tied to the classes on
// the element, which are styling.
//
// Where the row itself lives is the other half, and it is a separate question
// from where the markers are read. The row has to sit inside that one section,
// so a row moved elsewhere is a refusal rather than a pin that stays green
// while the section this package is documented against no longer states the
// set. What that establishes is that the row is inside <section id="surfaces">,
// by that section's id; it does not establish the section's number, which is
// why these comments name the section rather than its position in the
// document, and why a section inserted earlier in the PRD leaves every one of
// them true.
//
// The build declares an outcome by having a member in machine.Outcomes, whose
// group is machine.Outcome.Terminal and whose action is
// machine.Outcome.NextAction. There is no second vocabulary here for the build
// to speak: the set it already exports is the declaration.
//
// # Why the order is compared
//
// The row and the set are both closed sets a driving agent is written
// against, and both are read in their order by a person deciding whether they
// match. Comparing membership alone would let the two drift into different
// orders, where reading them side by side stops being how a reader checks
// them. The two groups are compared for the same reason: the row states them
// as two groups of four and two, and a set that alternates between the groups
// is one where that prose has come apart from the markers.
//
// # How it runs
//
// TestTheOutcomeSetIsPinnedToThePRD is the check. It runs under `make check`
// and under CI on every platform with everything else, so an outcome added to
// either side alone turns a branch red with nothing further wired.
//
// # Residual gaps
//
// Meaning past the group is not checked, and this is the largest gap. The row
// gives every outcome a sentence saying what it means, and nothing here reads
// one. If checks-passed came to mean something else entirely on the wire, this
// stays green as long as it is still declared, in the same place in the row,
// and in the group that says a run is finished with.
//
// The translation is not checked. machine.OutcomeOf maps a store.RunStatus and
// a graph.Status onto the set, and PRD section 9 names neither of those
// vocabularies, so the document holds nothing to pin that mapping against. The
// drift that produced this package was a translation defect as well as a
// membership one, and only the membership half of it is caught here.
//
// A set that is wrong on both sides is agreement. This compares two
// statements and never either against the behaviour they describe, so the
// state the machine interface was in before executing existed - neither side
// naming it, both sides agreeing - passes.
//
// The answer's fields are not checked, and there is nothing in the document to
// check them against: section 9 describes the answer in prose and does not
// enumerate the fields of one, which is deliberate, because a PRD that listed
// them would be a second owner of internal/machine's shapes. So a field added
// to machine.Run, and a next action a surface writes for itself rather than
// taking from the outcome, both pass here. Neither is hypothetical: a next
// action composed at the surface that builds an answer, rather than taken from
// the outcome, is a shape this repository has produced, and this package is
// green on it.
//
// The check reads the document's source rather than what a browser renders
// from it, so a marker inside a comment or a script string would be read as a
// declaration.
//
// # The second claim class
//
// internal/principles is the first, and it left the shape unnamed on purpose,
// because one instance is not enough to know which parts generalize. Two are
// still not many, and what the two have in common is worth writing down: a
// document that owns a closed set, an extraction rule a reader can apply by
// eye, a refusal rather than a short list, and a written statement of what
// agreement does not establish. What differs is the whole of the rest. That
// package needed a vocabulary for tests to speak claims in, and this one needs
// none, because the build already exports the set it declares.
package outcomes
