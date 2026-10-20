package safety

import (
	"context"
	"errors"
	"slices"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Update is what a run proposes: a target, the commit it wants that target to
// name, and the observation it took of the target before doing its work.
type Update struct {
	// Target is the branch to move.
	Target Target
	// Proposed is the commit the run verified and wants the target to name.
	// It is resolved in the local repository, so it has to exist there.
	Proposed string
	// Anchor is this run's observation of Target, from Guard.Observe. An
	// anchor that is not an observation of this target is
	// ErrAnchorNotObserved.
	Anchor Observation
	// Basis names the commits the run worked from: the commit submitted to
	// it, and the commit an earlier update by the same run left on Target,
	// where there was one. Guard.Decide allows an update that is not a
	// fast-forward only where every commit it drops is reachable from one of
	// them, so what it drops is history the run held and rewrote rather than
	// history somebody else put on the target. Empty means the run worked
	// from nothing it can name, and then Decide refuses every such update.
	// Guard.DecideFastForward and Adoption.Decide do not read it: the first
	// allows no drop, and the second allows one on its equivalence proof.
	//
	// Each entry is resolved in the local repository, so it has to exist
	// there; one that does not is ReasonUnverifiable. Which commits a run
	// worked from is the caller's word, read from the record the run keeps,
	// so it carries the provenance of that record and nothing more: an entry
	// naming the target's current commit permits dropping everything the
	// target holds, and nothing here can tell it from one the run received.
	Basis []string
}

// Kind says what shape of update was allowed.
type Kind string

const (
	// KindCreate is the target does not exist and the update creates it. The
	// lease is that it is still absent.
	KindCreate Kind = "create"
	// KindFastForward is the target's commit is contained in the proposed
	// commit, so the update discards nothing whatever else is true.
	KindFastForward Kind = "fast-forward"
	// KindAnchoredForce is the target still names the commit the run
	// observed, and the proposed commit does not contain it, so the update
	// drops commits the branch holds. Decision.Rewritten names every one of
	// them, and what licenses dropping them depends on which decision
	// allowed it: Decision.Rewritten states both.
	KindAnchoredForce Kind = "anchored-force"
)

// Decision is permission to perform one update, and it is only ever produced
// by this package's three decisions: Guard.Decide, Guard.DecideFastForward
// and Adoption.Decide. Its fields are unexported and it carries no setter, so a caller
// cannot assemble one, and cannot widen one it was given into permission for a
// different target or a different commit.
//
// The permission is inseparable from its anchor. Anchor is the state the
// decision was made against, and performing the update without leasing on it
// is performing a different update than the one that was allowed. The zero
// Decision is not permission: Allowed reports false for it.
type Decision struct {
	kind      Kind
	target    Target
	proposed  string
	anchor    Observation
	rewritten []string
	allowed   bool
}

// Allowed reports whether this value is permission to update. The zero
// Decision reports false, so a caller that ignored the error alongside it
// still has nothing it can act on.
func (d Decision) Allowed() bool { return d.allowed }

// Kind returns what shape of update was allowed.
func (d Decision) Kind() Kind { return d.kind }

// Target returns the branch this decision permits moving, and nothing else.
func (d Decision) Target() Target { return d.target }

// Proposed returns the commit this decision permits the target to be moved
// to, and nothing else.
func (d Decision) Proposed() string { return d.proposed }

// Anchor returns the observation the decision was made on. Its State is the
// lease the update has to be performed with: for KindCreate the target must
// still be absent, and otherwise it must still name Anchor.State().Commit.
//
// This is the whole of P6's anchor rule as this package can express it. A
// caller holding a Decision has the anchor in hand and no separate route to
// permission, so there is no allow path here that leaves the anchor behind.
func (d Decision) Anchor() Observation { return d.anchor }

// Rewritten names the commits the target holds that the proposed commit does
// not contain, and that this update therefore drops from the branch. It is
// empty for KindCreate and KindFastForward.
//
// What is enforced for every producer is that the target still names the
// commit the run observed, so the drop is anchored to a state the run saw, and
// that every commit the update drops is named here. What licenses the drop is
// the producer's, and the two that allow one guarantee different things:
//
//   - Through Guard.Decide, every commit dropped is reachable from a commit in
//     Update.Basis, so it is history the run worked from and its own work
//     rewrote. Its content is not checked: a rewrite that did not carry a
//     commit's change forward drops that change.
//   - Through Adoption.Decide, Update.Basis is not read, and a dropped commit
//     need not be reachable from anything the run held. What it guarantees
//     instead is the equivalence proof: merging the observed commit into the
//     proposed one over their one merge base reproduces the proposed tree, so
//     the net change the dropped commits brought, measured from that merge
//     base, is already in the proposed tree. Their messages, authorship and
//     commit boundaries are not kept.
//
// Guard.DecideFastForward allows no drop, so it never returns one. Who wrote
// the dropped commits is not established by either. A caller reports this
// list; it does not have to act on it.
//
// Each call returns a fresh slice, so a holder cannot edit the record of what
// this decision drops.
func (d Decision) Rewritten() []string { return slices.Clone(d.rewritten) }

// String renders an allowed decision as "<kind> <ref>@<remote> to <proposed>
// anchored on <anchor state>", where the anchor state is the commit the target
// named when the run observed it, or "absent". A value that is not permission,
// including the zero Decision, renders as "no decision".
func (d Decision) String() string {
	if !d.allowed {
		return "no decision"
	}
	return string(d.kind) + " " + d.target.String() + " to " + d.proposed + " anchored on " + d.anchor.State().String()
}

// Decide answers whether u may proceed, and on what anchor. It takes a fresh
// read of the target itself and compares it against the anchor the run
// observed.
//
// It allows an update in exactly three cases, and refuses everything else:
//
//   - The run observed the target absent and it is still absent. The update
//     creates it, leased on absence.
//   - The run observed a commit, the target still names it, and the proposed
//     commit contains it. Nothing on the target is lost.
//   - The run observed a commit, the target still names it, the proposed
//     commit does not contain it, and every commit the target holds that the
//     proposed commit does not contain is reachable from a commit in
//     Update.Basis. The update drops those commits, anchored to the state the
//     run observed. Decision.Rewritten names them.
//
// Everything else is a *Refusal. A target the run observed holding a commit
// that neither the proposed commit nor any commit the run worked from
// contains is ReasonWouldDiscard, naming exactly those commits, even though
// the target has not moved: the anchor describes the target and a lease on it
// would hold, which is why the lease alone protects nothing there. That is a
// branch somebody else pushed to before the run observed it, whether the
// run's commit diverged from it or stands behind it.
//
// A target that moved since the observation is refused whether or not
// commits would be lost, because an anchor that does not describe the target
// protects nothing. A fact that could not be
// established is refused rather than assumed: an unreadable remote, a
// revision that does not resolve, a comparison git could not answer, and two
// histories with no common ancestor are all refusals, and none of them has a
// default.
//
// Every reachability comparison is answered from the local repository, so a
// commit that repository does not hold cannot be resolved, and the comparison
// that needed it is unanswerable. That is ReasonUnverifiable, and which
// result it displaces depends on the path it lands on.
//
// On a target that moved, the commit compared is the one the fresh read
// found. Unresolvable, it displaces the ReasonWouldDiscard that would have
// named the commits the update drops, or the ReasonTargetMoved that would
// have reported the move with nothing dropped. On a target that did not move,
// the commit compared is the one the run observed. Unresolvable, it displaces
// the KindFastForward or KindAnchoredForce that would have been allowed. Both
// end at a refusal, so neither costs work.
//
// The precondition is that the local repository holds the commit the target
// names, at the observation and at the fresh read alike. A caller that cannot
// rely on already holding it fetches the target before deciding.
func (g *Guard) Decide(ctx context.Context, u Update) (Decision, error) {
	decision, err := g.decideShape(ctx, u)
	if err != nil {
		return Decision{}, err
	}
	if decision.kind != KindAnchoredForce {
		return decision, nil
	}
	if err := g.requireBasis(ctx, u, decision); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// decideShape is Decide without the basis rule: the anchor, the fresh read,
// and the shape of the update, with an anchored force allowed over whatever
// the observed target holds. Decide narrows it with requireBasis,
// DecideFastForward refuses every shape but a fast-forward over it, and
// Adoption.Decide allows an anchored force over it only on its equivalence
// proof, so no caller outside this package reaches an anchored force that
// none of the three narrowed.
func (g *Guard) decideShape(ctx context.Context, u Update) (Decision, error) {
	if err := u.Target.validate(); err != nil {
		return Decision{}, err
	}
	if u.Proposed == "" {
		return Decision{}, ErrInvalidUpdate
	}
	if !u.Anchor.Observed() || u.Anchor.Target() != u.Target {
		// An anchor that is not an observation of this target is a caller
		// bug, not a state of the world, so it is not reported as a refusal.
		return Decision{}, ErrAnchorNotObserved
	}

	proposed, err := g.git.ResolveCommit(ctx, u.Proposed)
	if err != nil {
		return Decision{}, &Refusal{
			Reason: ReasonUnverifiable,
			Target: u.Target,
			Anchor: u.Anchor,
			Detail: resolveCommitDetail("the proposed commit "+u.Proposed, "locally", err,
				"so what it contains cannot be established"),
			Cause: err,
		}
	}

	current, err := g.readTarget(ctx, u.Target)
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			refusal.Anchor = u.Anchor
		}
		return Decision{}, err
	}

	if current != u.Anchor.State() {
		return Decision{}, g.refuseMoved(ctx, u, proposed, current)
	}

	if !current.Exists {
		return Decision{
			kind:     KindCreate,
			target:   u.Target,
			proposed: proposed,
			anchor:   u.Anchor,
			allowed:  true,
		}, nil
	}

	rewritten, err := g.dropped(ctx, u, current, proposed)
	if err != nil {
		return Decision{}, err
	}
	kind := KindFastForward
	if len(rewritten) > 0 {
		kind = KindAnchoredForce
	}
	return Decision{
		kind:      kind,
		target:    u.Target,
		proposed:  proposed,
		anchor:    u.Anchor,
		rewritten: rewritten,
		allowed:   true,
	}, nil
}

// requireBasis refuses an anchored force that would drop a commit no commit in
// Update.Basis contains. The commits it names are the decision's rewritten
// commits less every one reachable from a basis commit, in the decision's
// order, so the refusal names what would be lost and not what the run's own
// work rewrote.
//
// A basis commit that does not resolve, or a comparison git could not
// answer, is ReasonUnverifiable rather than a smaller list: an entry that
// could not be compared contains nothing as far as this package can say.
func (g *Guard) requireBasis(ctx context.Context, u Update, d Decision) error {
	current := u.Anchor.State()
	unheld := slices.Clone(d.rewritten)
	for _, basis := range u.Basis {
		if len(unheld) == 0 {
			break
		}
		resolved, err := g.git.ResolveCommit(ctx, basis)
		if err != nil {
			return &Refusal{
				Reason:   ReasonUnverifiable,
				Target:   u.Target,
				Anchor:   u.Anchor,
				Observed: current,
				Detail: resolveCommitDetail("the commit "+basis+" the run worked from", "locally", err,
					"so what it contains of "+u.Target.Ref+" cannot be established"),
				Cause: err,
			}
		}
		outside, err := g.commitsNotIn(ctx, u, current, resolved)
		if err != nil {
			return err
		}
		unheld = slices.DeleteFunc(unheld, func(c string) bool { return !slices.Contains(outside, c) })
	}
	if len(unheld) == 0 {
		return nil
	}
	return &Refusal{
		Reason:    ReasonWouldDiscard,
		Target:    u.Target,
		Anchor:    u.Anchor,
		Observed:  current,
		Discarded: unheld,
		Detail: u.Target.Ref + " holds commits that " + d.proposed + " does not contain and that no " +
			"commit this run worked from contains, so replacing it would lose them",
	}
}

// refuseMoved builds the refusal for a target that no longer stands where the
// run observed it. The update is refused either way; the work here is to name
// every commit the target now holds that the proposed commit does not contain,
// because that is what a person needs in order to decide what to do next.
//
// That list is not narrowed to commits the run never saw. The anchor no longer
// describes the target, so the run cannot claim to have incorporated anything,
// and naming everything the update would drop is the conservative answer.
//
// A comparison that cannot be answered stays a refusal, and one that could not
// be answered is reported as unverifiable rather than as an empty list of
// discarded commits. An empty list would read as "nothing would be lost",
// which is the assumption this package exists to refuse.
func (g *Guard) refuseMoved(ctx context.Context, u Update, proposed string, current RemoteState) error {
	if !current.Exists {
		return &Refusal{
			Reason:   ReasonTargetMoved,
			Target:   u.Target,
			Anchor:   u.Anchor,
			Observed: current,
			Detail: observedClause(u) +
				" and the remote no longer advertises it, so the anchor cannot be honored",
		}
	}
	discarded, err := g.dropped(ctx, u, current, proposed)
	if err != nil {
		return err
	}
	reason, detail := ReasonTargetMoved, observedClause(u)+
		" and it now stands at "+current.Commit+", so the anchor does not describe what would be updated"
	if len(discarded) > 0 {
		reason = ReasonWouldDiscard
		detail = u.Target.Ref + " holds commits that " + proposed + " does not contain"
	}
	return &Refusal{
		Reason:    reason,
		Target:    u.Target,
		Anchor:    u.Anchor,
		Observed:  current,
		Discarded: discarded,
		Detail:    detail,
	}
}

// observedClause renders where the run saw the target as the opening of a
// sentence a caller can report unedited. An observed absence is a state rather
// than a missing commit, so it reads as observed absent rather than as
// observed at a state named absent. Every refusal that names the anchor builds
// its sentence from here, so the two cases cannot drift apart.
func observedClause(u Update) string {
	if !u.Anchor.State().Exists {
		return "the run observed " + u.Target.Ref + " absent"
	}
	return "the run observed " + u.Target.Ref + " at " + u.Anchor.State().Commit
}

// dropped names the commits the fresh read found on the target that
// incorporated does not contain. It establishes that the two are related
// first, because a comparison across unrelated histories answers a question
// nobody asked.
//
// Both the allow path and refuseMoved go through here, so a rule added to this
// sequence applies to both and cannot end up relaxed on one of them.
func (g *Guard) dropped(ctx context.Context, u Update, current RemoteState, incorporated string) ([]string, error) {
	if err := g.requireRelated(ctx, u, current, incorporated); err != nil {
		return nil, err
	}
	return g.commitsNotIn(ctx, u, current, incorporated)
}

// requireRelated refuses when the commit the fresh read found on the target
// and the proposed commit share no ancestor. Reachability
// between unrelated histories is answerable, and the answer is uninformative:
// every commit of one is missing from the other, so the comparison this
// package makes would report a rewrite of a branch that was never this
// branch. There is no default worth returning, so this refuses.
func (g *Guard) requireRelated(ctx context.Context, u Update, current RemoteState, b string) error {
	a := current.Commit
	_, err := g.git.MergeBase(ctx, a, b)
	if err == nil {
		return nil
	}
	if errors.Is(err, vcs.ErrNoMergeBase) {
		return &Refusal{
			Reason:   ReasonUnrelatedHistories,
			Target:   u.Target,
			Anchor:   u.Anchor,
			Observed: current,
			Detail:   a + " and " + b + " share no common ancestor, so neither can be said to contain the other's work",
			Cause:    err,
		}
	}
	return &Refusal{
		Reason:   ReasonUnverifiable,
		Target:   u.Target,
		Anchor:   u.Anchor,
		Observed: current,
		Detail:   "whether " + a + " and " + b + " are related could not be determined",
		Cause:    err,
	}
}

// commitsNotIn reports the commits reachable from the commit the fresh read
// found on the target and not from incorporated, and turns a failed comparison
// into a refusal rather than into an empty result. A refusal it builds carries
// that fresh read as Observed, because the read did succeed.
func (g *Guard) commitsNotIn(ctx context.Context, u Update, current RemoteState, incorporated string) ([]string, error) {
	have := current.Commit
	commits, err := g.git.CommitsNotIn(ctx, have, incorporated)
	if err != nil {
		return nil, &Refusal{
			Reason:   ReasonUnverifiable,
			Target:   u.Target,
			Anchor:   u.Anchor,
			Observed: current,
			Detail:   "what " + incorporated + " contains of " + have + " could not be determined",
			Cause:    err,
		}
	}
	return commits, nil
}
