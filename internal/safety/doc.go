// Package safety owns one question: may this branch update proceed, and on
// what anchor. It is the policy layer over internal/vcs, which provides the
// mechanism and deliberately implements none of this. PRD principle P6 is the
// contract, and PRD section 5's push stage is where it is used.
//
// Nothing here moves a reference or pushes. A caller receives a Decision or a
// *Refusal, and performing the update is its own job.
//
// # The anchor is an observation, not a commit identifier
//
// P6 names one trap outright: anchoring a lease to the tip you just read
// always succeeds and therefore protects nothing. This package answers it by
// refusing to accept a commit identifier as an anchor at all. Update.Anchor is
// an Observation, Guard.Observe is the only way to obtain one, and Observe
// reads the remote itself. A caller never holds a freshly read tip in a form
// it could pass, because the only way to get one is a read this package took
// and stamped with the target it was taken against.
//
// That is the "made unrepresentable" side of the choice rather than the
// "rejected at decision time" side, and the reason is that the wrong anchor
// cannot be recognized by its value. When the remote has not moved, the
// correct anchor and the tip read a moment before pushing are the same string.
// Only where the value came from tells them apart, so provenance is what the
// type carries.
//
// The residual gap is real and is not papered over. A caller that calls
// Observe and Decide back to back gets an anchor as worthless as the one P6
// warns about, and no rule inside this package can distinguish that from a run
// that observed the target, did its work, and then decided. What is enforced
// here is that the anchor is an observation of the target being updated and
// that every decision is taken against a read made at decision time. When the
// observation was taken belongs to the calling stage, which is why PRD section
// 11 asserts on the anchor value rather than on the outcome of a push.
//
// # What is allowed
//
// Guard.Decide allows three shapes and refuses everything else:
//
//   - KindCreate. The run observed the target absent and it is still absent.
//   - KindFastForward. The target still names the observed commit, and the
//     proposed commit contains it, so nothing on the branch is lost.
//   - KindAnchoredForce. The target still names the observed commit, and the
//     proposed commit does not contain it. The commits dropped are ones the
//     run observed, so this is the run rewriting what it submitted rather than
//     discarding work it never saw. Decision.Rewritten names them.
//
// A target that moved since the observation is refused whether or not commits
// would be lost. Where loss was identified the refusal names the commits, and
// where it was not, the refusal still stands: an anchor that does not describe
// the current target protects nothing, so proceeding on it would be a blind
// force wearing a lease.
//
// # Fail closed on an unverifiable fact
//
// Every fact a decision rests on is either established or the update is
// refused. A remote that cannot be read, a revision that does not resolve
// locally, a reachability comparison git could not answer, and two histories
// with no common ancestor are all refusals carrying ReasonUnreadableRemote,
// ReasonUnverifiable, or ReasonUnrelatedHistories. None of them has a default
// and none of them degrades to an allow.
//
// The rule has one edge worth naming, because it is where it would be easiest
// to relax by accident. When a comparison fails, this package does not report
// an empty list of commits that would be lost. An empty list reads as "nothing
// would be lost", which is precisely the assumption a failed comparison does
// not support, so the refusal says the comparison could not be answered
// instead.
//
// # A refusal is a typed result
//
// *Refusal carries the reason, the target, the anchor, what the fresh read
// found, and the commits that would be discarded. Every refusal matches
// ErrRefused with errors.Is, and errors.As reaches the detail. A refusal is
// never a logged warning execution continues past: there is no Decision inside
// it, and Decision.Allowed reports false for the zero value, so a caller that
// dropped the error still holds nothing it can act on.
package safety
