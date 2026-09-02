package safety

import (
	"context"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Target names the reference an update would move: a remote this repository
// can address, and a full reference name on it.
//
// The reference is required to be a full refs/ name because a short name is
// ambiguous on the wire. A remote can advertise refs/heads/x and refs/tags/x
// at once, and a policy that read the wrong one would be deciding about a
// reference nobody is updating.
//
// Which reference it is stays the caller's choice, and nothing here
// establishes that it is a branch. A remote advertises a lightweight tag as a
// name and the commit it names, which is exactly how it advertises a branch,
// so the two are not distinguishable in what this package reads back, and a
// caller that points a Target at one gets a decision about it. That is not a
// hole a run loses work through, because the lease and the reachability
// comparisons read the same commit either way; it is a statement that picking
// the reference is not a check performed here.
type Target struct {
	// Remote is a configured remote name or a URL, as git ls-remote takes it.
	Remote string
	// Ref is the full reference name, such as refs/heads/main.
	Ref string
}

// String renders the target as ref@remote, for a message a person reads.
func (t Target) String() string { return t.Ref + "@" + t.Remote }

// validate refuses a target that could not address one reference on one
// remote. It refuses before any git invocation.
func (t Target) validate() error {
	switch {
	case t.Remote == "", strings.HasPrefix(t.Remote, "-"):
		return ErrInvalidTarget
	case !strings.HasPrefix(t.Ref, "refs/"), strings.HasSuffix(t.Ref, "/"):
		return ErrInvalidTarget
	case strings.ContainsAny(t.Ref, "\x00\n *?[\\^~:"):
		return ErrInvalidTarget
	}
	return nil
}

// RemoteState is where a target stood at one moment: either it existed and
// named a commit, or it did not exist.
//
// Absent is a state rather than a missing value. An update onto a target that
// does not exist yet is a creation, and a target that stopped existing since
// the run looked is a change the run has to be told about, so both have to be
// representable.
type RemoteState struct {
	// Exists reports whether the remote advertised the target.
	Exists bool
	// Commit is the commit the target named, and is empty when Exists is
	// false.
	Commit string
}

// String renders the state as the commit it named, or as absent.
func (s RemoteState) String() string {
	if !s.Exists {
		return "absent"
	}
	return s.Commit
}

// Observation is where a target stood when the run actually looked at it. It
// is the only thing this package accepts as an anchor for an update.
//
// The zero Observation is not an observation, and its fields are unexported,
// so an anchor cannot be assembled field by field out of a commit identifier.
// Two constructors produce one: Observe, which reads the remote itself, and
// RestoreObservedFromCheckpoint, which rebuilds the observation an earlier
// stage of the same run recorded.
//
// That second constructor means the wrong anchor is representable. Where the
// guarantee rests instead is stated on RestoreObservedFromCheckpoint and in
// the package documentation: on the integrity of the checkpoint the record
// came out of, not on this type. What this type still enforces is that an
// anchor names the target it was taken against and carries a state a read
// could have produced.
//
// The residual gap, stated rather than implied away: a caller that calls
// Observe immediately before Decide, or that builds a record out of a live
// read, gets an anchor as worthless as the tip read a moment before pushing,
// and no rule inside this package can tell either apart from a run that
// observed the target, did its work, and then decided. When the anchor was
// taken is the calling stage's responsibility, and PRD section 13 asserts on
// the anchor value for exactly that reason.
type Observation struct {
	target   Target
	state    RemoteState
	observed bool
}

// Target returns the target this observation was taken against.
func (o Observation) Target() Target { return o.target }

// State returns where the target stood when the observation was taken. This
// is the anchor an update is decided on and the lease a caller performs it
// with.
func (o Observation) State() RemoteState { return o.state }

// Observed reports whether this value came from Observe or from
// RestoreObservedFromCheckpoint. The zero Observation reports false, and
// submitting it as an anchor is ErrAnchorNotObserved.
func (o Observation) Observed() bool { return o.observed }

// Record returns the durable form of this observation, for a run that has to
// carry its anchor across a restart. The zero Observation produces a record
// that names no target, which RestoreObservedFromCheckpoint refuses.
func (o Observation) Record() ObservationRecord {
	return ObservationRecord{
		Remote: o.target.Remote,
		Ref:    o.target.Ref,
		Exists: o.state.Exists,
		Commit: o.state.Commit,
	}
}

// String renders the observation as target=state, or as the zero value.
func (o Observation) String() string {
	if !o.observed {
		return "unobserved"
	}
	return o.target.String() + "=" + o.state.String()
}

// ObservationRecord is the durable form of an Observation. Every field is a
// plain scalar, so a checkpoint store whose values are text and booleans can
// carry one without this package knowing anything about that store.
//
// A record is written by Observation.Record and read back by
// RestoreObservedFromCheckpoint. It is not itself an anchor: it carries no
// provenance, and the trust that it is the one this run wrote belongs to
// whatever kept it.
type ObservationRecord struct {
	// Remote is the remote the observation was taken against.
	Remote string
	// Ref is the full reference name the observation was taken against.
	Ref string
	// Exists reports whether the remote advertised the target at that moment.
	Exists bool
	// Commit is the commit the target named, and is empty when Exists is
	// false.
	Commit string
}

// RestoreObservedFromCheckpoint rebuilds the observation an earlier stage of
// this run recorded, so a run interrupted between observing its target and
// deciding can carry its anchor across the restart. Without it a restarted
// push stage could only call Observe again, which is the tip read a moment
// before pushing that PRD principle P6 names outright.
//
// The name says what is trusted. This package cannot tell a record a run wrote
// before doing its work from one built out of a tip read a moment ago, so the
// provenance guarantee does not live here: it lives in the checkpoint the
// record came out of. A caller may pass only a record read back from a
// validated checkpoint, and may never build one from a live read.
//
// It refuses a record that could not have come from a read: ErrInvalidTarget
// when the record names no usable target, and ErrInvalidObservationRecord when
// an absent target names a commit or a present one names none.
func RestoreObservedFromCheckpoint(rec ObservationRecord) (Observation, error) {
	target := Target{Remote: rec.Remote, Ref: rec.Ref}
	if err := target.validate(); err != nil {
		return Observation{}, err
	}
	if rec.Exists == (rec.Commit == "") {
		return Observation{}, ErrInvalidObservationRecord
	}
	return Observation{
		target:   target,
		state:    RemoteState{Exists: rec.Exists, Commit: rec.Commit},
		observed: true,
	}, nil
}

// Guard applies the data-loss rules of PRD principle P6 over a git mechanism.
// It reads the remote and decides; it never moves a reference, and it has no
// way to.
type Guard struct {
	git Git
}

// New returns a Guard that reads and compares through git. It panics when git
// is nil, because a Guard that cannot read the remote could only ever refuse,
// and a guard that always refuses is a guard nobody keeps.
func New(git Git) *Guard {
	if git == nil {
		panic("safety: New requires a Git implementation")
	}
	return &Guard{git: git}
}

// Observe reads where target stands now and records it as this run's
// observation of it. The run does its work, and passes the result back as the
// anchor of the update it later proposes.
//
// A remote that cannot be read is a *Refusal with ReasonUnreadableRemote, not
// an observation of an absent target. The difference matters: an absent target
// authorizes a creation, and a remote nobody could reach authorizes nothing.
func (g *Guard) Observe(ctx context.Context, target Target) (Observation, error) {
	if err := target.validate(); err != nil {
		return Observation{}, err
	}
	state, err := g.readTarget(ctx, target)
	if err != nil {
		return Observation{}, err
	}
	return Observation{target: target, state: state, observed: true}, nil
}

// readTarget performs one fresh read of target and reports what the remote
// advertises for it. Every failure on the way out is a refusal: not being able
// to say where a branch stands is the case P6 names, and there is no answer
// this package is willing to assume in its place.
//
// It asks for the peeled name alongside the name itself. Git.RemoteRefs
// states why: that is what this package does to receive the object a
// reference names and the object it peels to as two values rather than one,
// which is what the peel check below reads. A reference that peels to nothing
// else matches the second pattern with nothing, so the extra pattern costs a
// branch target nothing.
func (g *Guard) readTarget(ctx context.Context, target Target) (RemoteState, error) {
	refs, err := g.git.RemoteRefs(ctx, target.Remote, target.Ref, target.Ref+"^{}")
	if err != nil {
		return RemoteState{}, &Refusal{
			Reason: ReasonUnreadableRemote,
			Target: target,
			Detail: "the remote could not be read, so where " + target.Ref + " stands now is unknown",
			Cause:  err,
		}
	}
	var found *vcs.Ref
	for i := range refs {
		if refs[i].Name != target.Ref {
			// ls-remote matches a pattern against the tail of a reference
			// name, so a read for refs/heads/x can carry other references
			// back. Only an exact name is this target.
			continue
		}
		if found != nil {
			return RemoteState{}, &Refusal{
				Reason: ReasonUnverifiable,
				Target: target,
				Detail: "the remote advertised " + target.Ref + " more than once",
			}
		}
		found = &refs[i]
	}
	if found == nil {
		return RemoteState{}, nil
	}
	if found.Object == "" {
		// The Git interface is what this package decides against, and
		// *vcs.Repository is one implementation of it. That one rejects an
		// advertisement with no object while parsing, so this guard is what
		// the interface's contract rests on rather than what that
		// implementation reaches.
		return RemoteState{}, &Refusal{
			Reason: ReasonUnverifiable,
			Target: target,
			Detail: "the remote advertised " + target.Ref + " with no object",
		}
	}
	if found.Commit != found.Object {
		// The reference names one object and peels to another, which is an
		// annotated tag. The lease this package hands back compares against
		// the object the reference names, while the reachability comparisons
		// read the commit it peels to, so the two would be answering about
		// different objects. The read above asks for the peeled name so this
		// arrives rather than being folded away.
		//
		// That is what this rules out, and all of it: a reference that names
		// its commit directly is advertised the same way whether it is a
		// branch or a lightweight tag, so this does not establish that the
		// target is a branch.
		return RemoteState{}, &Refusal{
			Reason: ReasonUnverifiable,
			Target: target,
			Detail: "the remote advertised " + target.Ref + " as " + found.Object +
				", which peels to " + found.Commit + ", so it is not a branch",
		}
	}
	return RemoteState{Exists: true, Commit: found.Object}, nil
}
