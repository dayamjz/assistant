package safety

// RequireObservedAt refuses an observation that does not find its target
// standing at commit, the commit an update this run already performed left
// there. It is the anchor rule for a second update of a branch the run has
// updated once: PRD section 14 decision 7's rebase of a conflicted pull
// request, which rewrites a branch the push stage already forwarded.
//
// What it buys over Observe alone is the line between the run's own commits
// and anybody else's. A fresh observation anchors to whatever the target holds
// now, and an anchored force on it would drop a commit someone pushed after
// the run's first update as though the run had seen it before doing its work.
// Requiring the observation to name the commit the run left there means every
// commit an anchored force over it drops is one the target held when the run
// updated it, and a target anybody else moved since is refused rather than
// adopted. The refusal carries ReasonTargetMoved, so it matches ErrRefused and
// a caller relays it as it relays any other.
//
// It applies to an observation however it was produced, so a caller may ask
// it of a fresh Observe and again of the same observation restored from a
// checkpoint before deciding on it.
//
// What it does not establish, stated rather than implied. That commit is the
// one the run's earlier update wrote is the caller's word, read from the same
// checkpoint the anchor rests on, so it carries that anchor's provenance and
// nothing more. And a target that moved away and back again between the two
// updates is not seen; it names the same commit, so nothing it held then is
// lost now.
//
// The zero Observation is ErrAnchorNotObserved and an empty commit is
// ErrInvalidUpdate, because both are a caller's mistake rather than a state of
// the world.
func RequireObservedAt(o Observation, commit string) error {
	if !o.Observed() {
		return ErrAnchorNotObserved
	}
	if commit == "" {
		return ErrInvalidUpdate
	}
	if o.state.Exists && o.state.Commit == commit {
		return nil
	}
	return &Refusal{
		Reason: ReasonTargetMoved,
		Target: o.target,
		Anchor: o,
		Detail: o.target.Ref + " was observed at " + o.state.String() + " and not at " + commit +
			", the commit this run's earlier update left there, so what it holds is no longer " +
			"known to be only what this run put there",
	}
}
