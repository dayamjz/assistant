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
// refusing a bare commit identifier as an anchor. Update.Anchor is an
// Observation, its fields are unexported, and exactly two constructors produce
// one: Guard.Observe, which reads the remote itself, and
// RestoreObservedFromCheckpoint, which rebuilds one an earlier stage of the
// same run recorded.
//
// The anchor is a type rather than a checked value because the wrong anchor
// cannot be recognized by its value. When the remote has not moved, the
// correct anchor and the tip read a moment before pushing are the same string.
// Only where the value came from tells them apart, so provenance is what the
// type carries.
//
// # Where the provenance guarantee lives
//
// The wrong anchor is not unrepresentable. RestoreObservedFromCheckpoint takes
// an ObservationRecord of plain scalar fields, and nothing here can tell a
// record a run wrote before doing its work from one built out of a tip read a
// moment ago. That path exists because a run has to survive a restart: PRD
// section 5 puts the rebase at stage 2 and the push at stage 7, and section 13
// requires the run to stay correct across a kill at every stage boundary.
// Without a durable form, a restarted push stage could only call Observe
// again, which is the forbidden anchor exactly.
//
// So the guarantee moved rather than disappeared, and it is worth being exact
// about where it went. It rests on the PRD's rule that a checkpoint is never
// loaded from a source outside this home. That is a trust boundary, not an
// integrity check over the value: a record whose Commit was replaced with the
// current tip is the same shape as the one the run wrote, so it decodes and
// validates identically, and validating a checkpoint on read establishes
// nothing about which commit the record names. A caller that persists an
// ObservationRecord anywhere outside that boundary has given the guarantee up
// entirely.
//
// What this package still enforces is that the anchor names the target being
// updated, that a restored record describes a state a read could have
// produced, and that every decision is taken against a read Decide makes at
// decision time. A caller may restore only from inside that boundary, and may
// never build a record from a live read.
//
// The residual gaps are real and are not papered over. A caller that calls
// Observe and Decide back to back gets an anchor as worthless as the one P6
// warns about, a caller that fabricates a record gets the same, and no rule
// inside this package can distinguish either from a run that observed the
// target, did its work, and then decided. When the observation was taken
// belongs to the calling stage, which is why PRD section 13 asserts on the
// anchor value rather than on the outcome of a push.
//
// # What is allowed
//
// Guard.Decide allows three shapes and refuses everything else:
//
//   - KindCreate. The run observed the target absent and it is still absent.
//   - KindFastForward. The target still names the observed commit, and the
//     proposed commit contains it, so nothing on the branch is lost.
//   - KindAnchoredForce. The target still names the observed commit, and the
//     proposed commit does not contain it. The update drops the commits the
//     target holds that the proposed commit does not contain, and
//     Decision.Rewritten names every one of them. The drop is anchored to a
//     state the run observed; that the run wrote what is dropped is not
//     established, because this package never learns the run's base and so
//     cannot tell a commit that reached the target before the observation from
//     one the run submitted.
//
// A target that moved since the observation is refused whether or not commits
// would be lost. Where loss was identified the refusal names every commit the
// target holds that the proposed commit does not contain, without narrowing
// that to commits the run never observed: the anchor no longer describes the
// target, so nothing can be claimed to be incorporated. Where no loss was
// identified the refusal still stands, because an anchor that does not
// describe the current target protects nothing, so proceeding on it would be a
// blind force wearing a lease.
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
