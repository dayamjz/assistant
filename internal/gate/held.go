package gate

import (
	"context"
	"errors"
	"fmt"
)

// resolution says which gate the seam is being asked for. The two operations
// differ in this and in nothing else the seam does, so the difference is a
// value the seam reads rather than a second seam that could drift from this
// one.
type resolution int

const (
	// gateToRepair is the gate an initialization acts on. It is the gate the
	// working copy already names, when that path is a gate of this home, holds
	// a repository, and nobody else is bound to it; otherwise it is the gate
	// the working copy's own path hashes to, which is what a copy of a gated
	// project falls back to.
	gateToRepair resolution = iota
	// gateToRemove is the gate a removal acts on. It is the gate the working
	// copy names and nothing else: a removal that fell back to a gate the
	// working copy does not name would delete a repository on the strength of
	// a path hash.
	gateToRemove
)

// held is a gate an operation may act on, and the only way to obtain one is
// withGate. That is the point of the type.
//
// Everything an operation owes before it touches a gate is discharged by the
// seam that produces this value: the repository is resolved once and its
// contents read once, the ownership question is asked and refused on, and every
// gate the resolution looked at is left with an admission hook. An operation
// added later cannot skip any of it, because it cannot get a held without going
// through the seam, and no exported entry point here takes a gate's path: a
// Spec names the home and the working copy, and which gate those resolve to is
// the seam's to work out.
//
// This package kept meeting the opposite arrangement, where each obligation sat
// at the call that first needed it and the sibling call added afterwards did
// not have it: ownership checked on adoption and not on removal, hooks guarded
// on creation and not on repair, the no-open-gate invariant held by one
// operation and not the other. Those are all the same defect, and this type is
// the answer to it rather than a fifth reminder to remember.
type held struct {
	set         settings
	home        string
	workingPath string
	// copyOf is the working copy the operation was asked about, opened once so
	// that reading its remote and writing it are the same handle.
	copyOf WorkingCopy
	// id is the gate's identifier, taken from the directory it is filed under
	// and checked against the record when there is one.
	id string
	// repository is the gate's absolute path.
	repository string
	// holds is what that path holds, read once. A caller cannot be told a path
	// is a gate and then read a different answer out of it a moment later.
	holds contents
	// record is the gate's record, valid when holds is repositoryWithRecord.
	record record
	// reattached is what Gate.Reattached reports.
	reattached bool
}

// withGate is the seam every operation on a gate goes through. It resolves the
// gate the operation acts on, refuses when another working copy is bound to it,
// leaves no gate it looked at accepting pushes with nothing checking them, and
// only then runs the operation.
//
// The seal it owes is discharged before the operation begins rather than on the
// way out, and that ordering is the whole of the difference. A seal that fails
// is then a gate that was never acquired and an operation that has not started,
// so no unrelated gate's unreadable hooks directory can turn a completed
// operation into a reported failure. What this cannot cover is a repository
// that does not exist yet, and ensureRepository owns that one, because it is
// the one place a repository comes into existence.
func withGate(ctx context.Context, spec Spec, want resolution, opts []Option, do func(*held) error) error {
	set := resolveSettings(opts)
	if set.index == nil {
		return fmt.Errorf("%w: nothing here can say whether another working copy is bound to a gate without one, "+
			"and guessing is how a working copy takes over a gate whose history belongs to another; "+
			"pass gate.WithIndex with the store this home is opened against", ErrNoIndex)
	}
	home, workingPath, err := validatePaths(spec)
	if err != nil {
		return err
	}
	h, err := acquire(ctx, set, home, workingPath, want)
	if err != nil {
		return err
	}
	return do(h)
}

// acquire resolves the gate an operation acts on and hands back the handle.
//
// Every gate it looks at is sealed before it returns, whatever it returns, so a
// refusal here leaves a gate that refuses every push rather than one that
// accepts every push. It runs from a deferred call rather than from each
// refusal because the refusals are many and the next one added would not have
// carried it.
//
// A seal that fails is joined onto whatever was being returned rather than
// dropped behind it, so a refusal already in flight cannot report a gate as
// sealed that is not. Both stay matchable with errors.Is, and no handle comes
// back either way: an operation must not proceed on a gate this could not
// close.
//
// Sealing a gate this resolution is refusing to act on is deliberate, including
// one another working copy owns. The only gate it changes is one that was
// already accepting every push with nothing running, and leaving that alone to
// avoid touching somebody else's gate would be choosing the silent failure over
// the loud one.
func acquire(ctx context.Context, set settings, home, workingPath string, want resolution) (h *held, err error) {
	touched := touchedGates{home: home}
	defer func() {
		if sealErr := touched.seal(); sealErr != nil {
			h, err = nil, errors.Join(err, sealErr)
		}
	}()

	copyOf, err := set.open(ctx, workingPath)
	if err != nil {
		return nil, fmt.Errorf("gate: opening the working copy at %s: %w", workingPath, err)
	}
	named, err := boundRepository(ctx, copyOf)
	if err != nil {
		return nil, err
	}
	touched.add(named)

	base := held{set: set, home: home, workingPath: workingPath, copyOf: copyOf}
	if want == gateToRemove {
		return base.resolveNamed(ctx, named)
	}
	id, err := Identify(workingPath)
	if err != nil {
		return nil, err
	}
	touched.add(repositoryPath(home, id))
	return base.resolveRepairable(ctx, named, id)
}

// resolveNamed is the removal resolution: the gate the working copy names, or a
// refusal naming the step that succeeds from where the reader is.
func (h held) resolveNamed(ctx context.Context, named string) (*held, error) {
	if named == "" {
		return nil, fmt.Errorf("%w: %s has no %s remote", ErrNoGate, h.workingPath, RemoteName)
	}
	found, err := h.at(named)
	if err != nil {
		if errors.Is(err, ErrNotAGate) {
			return nil, fmt.Errorf("%w; nothing was removed. Removing the %s remote here detaches this working copy "+
				"and always succeeds, because the path it names is not a gate of this home for anything to be "+
				"lost from, and initializing %s afterwards gives it a gate of its own",
				err, RemoteName, h.workingPath)
		}
		return nil, err
	}
	if found.holds == noRepository {
		return nil, fmt.Errorf("%w: the %s remote of %s names %s, and nothing is there; "+
			"initialize %s to get a gate of its own, which repoints the remote, and remove that",
			ErrNotAGate, RemoteName, h.workingPath, named, h.workingPath)
	}
	if err := found.ensureUnclaimed(ctx, found.removalRemedy); err != nil {
		return nil, err
	}
	return found, nil
}

// resolveRepairable is the initialization resolution.
//
// The gate the working copy already names is kept when it is a gate of this
// home, holds a repository, and nobody else is bound to it. That is what
// carries a gate, its identifier, and everything recorded against that
// identifier across a move, and it is what keeps a gate that lost its record
// from being abandoned for a new empty one at the current path's hash, since
// nothing here scans the home for a gate nobody names.
//
// Every other case falls back to the gate the working copy's own path hashes
// to: a remote naming something that is not a gate of this home, a remote
// naming a repository that is gone and so has nothing to carry, and a gate
// somebody else is still bound to, which is exactly what a copy of a gated
// project inherits. The fallback is not a refusal because there is a gate to
// hand out; taking the one the copy inherited would be the refusal.
func (h held) resolveRepairable(ctx context.Context, named, id string) (*held, error) {
	own := repositoryPath(h.home, id)
	if named != "" && named != own {
		found, err := h.at(named)
		switch {
		case err != nil && !errors.Is(err, ErrNotAGate):
			return nil, err
		case err != nil:
			// The remote names something that is not a gate of this home, which
			// is a claim on nothing.
		case found.holds == noRepository:
			// The remote names a gate that is not there. There is nothing to
			// keep and nothing to carry across a move.
		default:
			holders, err := found.claimants(ctx)
			if err != nil {
				return nil, err
			}
			if len(holders) == 0 {
				// A record that already names this working copy is the ordinary
				// case a repeated initialization reaches, and is not a
				// reattachment.
				found.reattached = found.holds != repositoryWithRecord || found.record.WorkingPath != h.workingPath
				return found, nil
			}
		}
	}

	found, err := h.at(own)
	if err != nil {
		return nil, err
	}
	if found.holds == noRepository {
		// Nothing is there, so this initialization is the one creating the gate
		// and there is nobody to take it from.
		return found, nil
	}
	if err := found.ensureUnclaimed(ctx, found.repairRemedy); err != nil {
		return nil, err
	}
	return found, nil
}

// at fills in the gate at repo: what the path holds, the identifier it is filed
// under, and its record when it has one. It refuses with ErrNotAGate a path
// that is not one of this home's, and with ErrMalformedRecord a record whose
// identifier is not the one the gate is filed under.
//
// The identifier comes from the directory name checked against repositoryPath
// rather than from the record, because a gate whose record is gone still has
// one, a gate that does not exist yet already has one, and the two must agree
// when both are there.
func (h held) at(repo string) (*held, error) {
	rec, holds, err := gateRepository(h.home, repo)
	if err != nil {
		return nil, err
	}
	id := identifierAt(h.home, repo)
	if id == "" {
		return nil, fmt.Errorf("%w: %s is in %s but is not filed under an identifier",
			ErrNotAGate, repo, repositoriesDir(h.home))
	}
	if holds == repositoryWithRecord && rec.ID != id {
		return nil, malformedRecord(repo, "%s records identifier %q, which belongs at %s",
			repo, rec.ID, repositoryPath(h.home, rec.ID))
	}
	h.id, h.repository, h.holds, h.record = id, repo, holds, rec
	return &h, nil
}

// repairRemedy is what an initialization tells an operator whose own gate is
// held by somebody else. There is no second gate at this identifier to hand
// out, so the action that succeeds is the holder detaching.
func (h *held) repairRemedy(claimant string) string {
	return fmt.Sprintf("%s hashes to %s, so this home has no second gate to hand out for it. Taking this one over "+
		"takes it, and everything its runs recorded, away from a working copy that is still using it, so do this "+
		"only once you are satisfied the history in %s is not that working copy's. The gate is handed over as soon "+
		"as %s stops pointing at it, so removing the %s remote there detaches it and always succeeds, and "+
		"initializing %s again then takes the gate over",
		h.workingPath, h.id, h.repository, claimant, RemoteName, h.workingPath)
}

// removalRemedy is what a removal tells an operator holding somebody else's
// gate, which is what a copy of a gated project inherits. Detaching is the step
// that always succeeds, because it takes nothing away.
func (h *held) removalRemedy(claimant string) string {
	return fmt.Sprintf("nothing was removed; a working copy copied from %s inherits its %s remote, so if this is "+
		"such a copy, removing the %s remote here detaches it and always succeeds; the gate is %s's, and only a "+
		"removal run there can act on it",
		claimant, RemoteName, RemoteName, claimant)
}

// touchedGates is the set of gates a resolution has looked at, and the seal it
// owes each of them.
type touchedGates struct {
	home  string
	paths []string
}

// add records a path this resolution has learned about. A path that is not a
// gate of this home is filtered when the seal runs, not here, so a caller can
// hand over whatever it has.
func (t *touchedGates) add(repo string) {
	if repo == "" {
		return
	}
	for _, seen := range t.paths {
		if seen == repo {
			return
		}
	}
	t.paths = append(t.paths, repo)
}

// seal puts a refusing admission hook into every gate this resolution looked at
// that holds a repository and has no admission hook of its own. A gate that
// already has one, and a path that holds nothing or is not filed where this
// home keeps its gates, are left alone.
//
// One gate whose seal fails does not stop the others, and the failures are
// joined rather than reduced to the first. A gate left open because a different
// gate's hooks directory could not be written is the same invisible failure
// this exists to prevent.
func (t *touchedGates) seal() error {
	var errs error
	for _, repo := range t.paths {
		if identifierAt(t.home, repo) == "" || holdsNoRepository(repo) {
			continue
		}
		if err := sealAdmission(repo); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}
