// Package custody is PRD section 8's custody module: reconciling a person's
// local branch with what the pipeline pushed or holds, through three
// operations - classify, synchronize, recover - and the only path in this
// build that moves a person's branch. PRD section 11 is the contract.
//
// # Why a package of its own, over internal/safety
//
// Section 8 lists safety and custody as two modules, and the split keeps one
// owner per fact (P14). internal/safety owns whether one update may proceed
// and on what anchor - Decide, DecideFastForward, and the adoption proof and
// decision section 11's diverged row needs - and it moves nothing. This
// package owns the two facts safety does not: which of section 11's six
// classes a branch is in, read from the records that own each input, and
// the exact steps a class allows, carried out in order. It decides no update
// itself; every move it makes is one a safety decision allowed, carried out
// with internal/vcs' compare-and-swap. And it builds no command line:
// internal/vcs is the mechanism, so a rule here cannot be skipped by a call
// site that shells out.
//
// # Classify reads; nothing else in it writes
//
// Classify takes the facts its caller read from their owners - the run's
// row, its last checkpoint, its isolated copy, the working copy's branch -
// and asks two repositories questions: the gate repository, which holds the
// run's commits, and the working copy, which holds the person's. It fetches
// nothing and writes no reference, index or file in either. The one write
// anywhere is the equivalence proof's merge, which writes the merged tree's
// objects into the gate repository's object store; no reference names them.
// That is what lets assistant status report a classification and still never
// fetch and never mutate the person's checkout.
//
// # What each class is read from in this build
//
// PRD section 11 states the classes in terms of a run's termination. This
// build records a run's position in exactly one place, its checkpoint
// history (internal/checkpoints), and a run's copy may hold commits made
// after the last checkpoint was written. So "the head" is read from the last
// checkpoint's pipeline.KeyHead, replaced by the copy's own HEAD when the
// copy still stands at another commit, and "what was pushed" is the
// checkpoint's pipeline.KeyPushed. "The commit the run observed" is the run
// row's SubmittedHead, which is the anchor of every move here, restored
// through safety.RestoreObservedFromCheckpoint: it is the one observation of
// the person's branch a run makes, and a branch that moved off it since is
// refused rather than re-read, because the tip read a moment before writing
// is the anchor P6 names as protecting nothing.
//
// Yours already is "termination positively verified the head never moved".
// This build has no verification taken at termination; what it has is the
// run's own last checkpoint, read when the classification is asked for, and
// the copy if it still stands. A run whose checkpoint is missing or names no
// head is ReasonUnrecorded rather than read as unmoved, and a copy holding
// uncommitted changes is ReasonCopyUnclean, because both are where the
// positive verification would be and is not. This package also counts a
// branch that already contains the pipeline's work as yours already, which
// is the reading under which a second synchronization after a first one
// reports nothing to do.
//
// Recoverable brings the work back under RecoveredRef, beside the branch,
// and never moves the branch: the work is a finished run's, generally not
// validated, and taking it into the branch is the person's decision.
//
// Behind and diverged-but-equivalent need a pushed commit "freshly
// verified": Apply reads the code host's branch again before fetching
// anything and refuses with ReasonNotCurrent unless it still names the
// commit the run pushed. The commit itself is fetched from the gate
// repository by identifier, so nothing is fetched from the code host.
//
// # The never list
//
// Section 11 forbids stashing, merging, rebasing, forcing, switching
// branches, deleting a branch, and touching a remote. Working is the whole
// of what Apply can do to a working copy and has no operation for any of
// them: its writes are a fetch into Namespace, a create-only reference, the
// removal of the fetched scratch reference, and a compare-and-swap move of
// the one branch. The adoption move is not a force: it is anchored on the
// run's observation, allowed only on internal/safety's proof, and preceded by
// ReplacedRef naming the head it replaces. Reading the code host is a read;
// nothing is written there.
//
// # Residual gaps
//
//   - Only the newest run of a branch is classified. Work an older finished
//     run moved and nothing recovered is not reported once a newer run of the
//     branch exists.
//   - The classification is read when asked, not at termination, so a copy
//     removed after its run finished cannot be asked about, and the last
//     checkpoint is all that says where its work stood. Its removal was
//     internal/tasks' cleanup decision, which removes a copy only on a proof
//     that the commit it stood on landed, on that proof's own terms and with
//     its own residual gaps.
//   - A pushed commit whose code host branch has since moved on is refused
//     as not current, even where the person would want the newer tip; that
//     tip is not the run's work and no observation anchors it.
//   - Containment is asked of the working copy, and a commit it cannot
//     resolve is read as one its branch does not contain. A shallow working
//     copy is refused before that reading is made.
//   - Between the plan a person confirms and the steps Apply takes, nothing
//     is held: a branch that moved in between is refused by the decision's
//     fresh read, and a code host branch that moved is refused by the verify
//     step, but a working tree edited between ReadCheckout and the move is
//     what internal/vcs' MoveBranch states it re-checks.
package custody
