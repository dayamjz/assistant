package safety

import (
	"context"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Target names the branch an update would move: a remote this repository can
// address, and a full reference name on it.
//
// The reference is required to be a full refs/ name because a short name is
// ambiguous on the wire. A remote can advertise refs/heads/x and refs/tags/x
// at once, and a policy that read the wrong one would be deciding about a
// reference nobody is updating.
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
// The zero Observation is not an observation, and no exported field or
// constructor can turn a commit identifier into one. Observe is the only way
// to obtain one, and Observe reads the remote itself. That is the shape PRD
// principle P6 asks for: the caller never holds a live tip it could pass as
// the anchor, because the value it would have to pass has to come from a read
// it took earlier and kept.
//
// The residual gap, stated rather than implied away: a caller that calls
// Observe immediately before Decide gets an anchor as worthless as the tip
// read a moment before pushing, and no rule inside this package can tell that
// apart from a run that observed the target, did its work, and then decided.
// What is enforced here is that the anchor is an observation of this target
// and that the decision is made against a read taken after it. When the anchor
// was taken is the calling stage's responsibility, and PRD section 13 asserts
// on the anchor value for exactly that reason.
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

// Observed reports whether this value came from Observe. The zero Observation
// reports false, and submitting it as an anchor is ErrAnchorNotObserved.
func (o Observation) Observed() bool { return o.observed }

// String renders the observation as target=state, or as the zero value.
func (o Observation) String() string {
	if !o.observed {
		return "unobserved"
	}
	return o.target.String() + "=" + o.state.String()
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
func (g *Guard) readTarget(ctx context.Context, target Target) (RemoteState, error) {
	refs, err := g.git.RemoteRefs(ctx, target.Remote, target.Ref)
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
		return RemoteState{}, &Refusal{
			Reason: ReasonUnverifiable,
			Target: target,
			Detail: "the remote advertised " + target.Ref + " with no object",
		}
	}
	if found.Commit != found.Object {
		// The reference peels to something other than what it names, which is
		// an annotated tag. A branch never does, and the lease this package
		// hands back compares against the object the reference names, so a
		// target that is not a branch is refused rather than decided about.
		return RemoteState{}, &Refusal{
			Reason: ReasonUnverifiable,
			Target: target,
			Detail: "the remote advertised " + target.Ref + " as " + found.Object +
				", which peels to " + found.Commit + ", so it is not a branch",
		}
	}
	return RemoteState{Exists: true, Commit: found.Object}, nil
}
