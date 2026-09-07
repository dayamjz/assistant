package gate

import (
	"context"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/store"
)

// Index is the home's record of which working copies are bound to which gate.
// Every operation here needs one and none of them will proceed without it; see
// WithIndex and ErrNoIndex.
//
// It exists because of the shape PRD section 8 gives a gate. A gate is filed
// under a hash of its working copy's path, so this package can find a gate from
// a path and cannot find the paths from a gate. The question every operation
// here has to answer before it acts is the second one: is another working copy
// still bound to this gate. Without an index that question is answered by
// looking at the one record inside the gate and probing the one path it names,
// which says nothing at all when the record is gone.
//
// The interface is three methods wide because those are the three things this
// package does with the home's database. It is not a database handle: this
// package does not open the database, does not close it, and cannot write
// anything else into it, and a reader checks that by reading this declaration
// rather than by auditing the calls.
//
// *store.Store is the implementation this product uses, and the assertion below
// fails the build on the day it stops satisfying this.
type Index interface {
	// GateBindings returns every working copy the home records as bound to the
	// gate with this identifier. A gate nothing is bound to returns no
	// bindings and no error, because nobody being bound is an answer.
	GateBindings(ctx context.Context, gateID string) ([]store.GateBinding, error)
	// BindGate records that the working copy at workingPath is bound to the
	// gate with this identifier, replacing whatever binding that working copy
	// had.
	BindGate(ctx context.Context, workingPath, gateID string) (store.GateBinding, error)
	// UnbindGate removes a working copy's binding, and reports
	// store.ErrNotFound when there was none to remove. A removal treats that
	// as nothing left to do, the same way it treats a remote that is already
	// gone.
	UnbindGate(ctx context.Context, workingPath string) error
}

// The index this product supplies is the store, and the operations named above
// are its own. If any of them changes shape, this fails the build here rather
// than at the one call site that happens to be compiled first.
var _ Index = (*store.Store)(nil)

// ownership is a gate as the question "who does this belong to" needs it: the
// identifier the index is asked under, the path a candidate's assistant remote
// has to name, and the record read off the gate itself.
//
// It exists so that every operation asking that question asks one mechanism
// rather than gathering candidates of its own. held.claimants and
// WorkingCopyFor are the two askers, and each keeps its own question - who
// else holds this gate, and which single working copy holds it - while what
// counts as evidence is here, which is what P14 asks of a fact with two
// readers.
type ownership struct {
	set settings
	// id is what the gate is filed under, which is how the index is asked.
	id string
	// repository is the gate's absolute path.
	repository string
	// holds is what that path holds, and record is the gate's record, valid
	// when holds is repositoryWithRecord.
	holds  contents
	record record
}

// holders is the working copies other than exclude that are still bound to
// this gate.
//
// The candidates come from two places, and neither is believed on its own. The
// index names every working copy the home recorded as bound, which is the
// enumerable answer and the one a gate's own directory cannot give. The gate's
// record names the working copy the gate last belonged to, which survives the
// home's database being lost or replaced and so is not a duplicate of the index
// but the thing that outlives it. Every candidate is then checked against the
// present: a working copy counts only if it is still there and its own
// assistant remote still names this gate.
//
// Checking rather than believing is what keeps the two sources from becoming
// two owners of one fact. Neither decides anything. What they contribute is the
// list of working copies worth asking about, and the answer comes from asking.
//
// exclude is the working copy asking, which is never its own holder. A caller
// asking on nobody's behalf passes the empty string, which excludes nothing a
// candidate could be: stillBound answers not bound for a path that is not a
// directory, and the empty path is not one.
func (o ownership) holders(ctx context.Context, exclude string) ([]string, error) {
	bindings, err := o.set.index.GateBindings(ctx, o.id)
	if err != nil {
		return nil, fmt.Errorf("gate: reading which working copies are bound to gate %s: %w", o.id, err)
	}
	candidates := make([]string, 0, len(bindings)+1)
	for _, binding := range bindings {
		candidates = append(candidates, binding.WorkingPath)
	}
	if o.holds == repositoryWithRecord {
		candidates = append(candidates, o.record.WorkingPath)
	}

	// A candidate named by both sources is asked about once.
	seen := map[string]struct{}{exclude: {}}
	var holders []string
	for _, candidate := range candidates {
		if _, asked := seen[candidate]; asked {
			continue
		}
		seen[candidate] = struct{}{}
		bound, err := stillBound(ctx, o.set, candidate, o.repository)
		if err != nil {
			return nil, err
		}
		if bound {
			holders = append(holders, candidate)
		}
	}
	return holders, nil
}

// claimants is the working copies other than this one that are still bound to
// this gate. An empty answer is what makes a gate this operation's to act on.
//
// What counts as evidence is ownership.holders' and not this operation's, so
// the working copy asking is the whole of what this adds.
func (h *held) claimants(ctx context.Context) ([]string, error) {
	return h.ownership().holders(ctx, h.workingPath)
}

// ownership is this gate as the shared evidence rule needs it.
func (h *held) ownership() ownership {
	return ownership{
		set:        h.set,
		id:         h.id,
		repository: h.repository,
		holds:      h.holds,
		record:     h.record,
	}
}

// ensureUnclaimed refuses with ErrGateClaimed when another working copy is
// still bound to this gate. The remedy differs by operation and is the caller's
// to word; who holds a gate is not, and this is the one place either operation
// asks.
func (h *held) ensureUnclaimed(ctx context.Context, remedy func(claimant string) string) error {
	holders, err := h.claimants(ctx)
	if err != nil {
		return err
	}
	if len(holders) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the gate at %s belongs to the working copy at %s, which still points at it, "+
		"and %s is not that working copy; %s",
		ErrGateClaimed, h.repository, strings.Join(holders, ", "), h.workingPath, remedy(holders[0]))
}

// stillBound reports whether the working copy at claimant is still there and
// still names the gate at repo. A working copy that moved leaves nothing
// behind, so the gate is free; a working copy that was copied leaves the
// original behind, so the gate is not.
//
// A claimant that is not there or will not resolve counts as not bound, and so
// does one that will not open. Which way those fail is a decision rather than
// an accident. Treating an unreadable claimant as still bound would give a
// moved working copy a second gate and orphan the run history recorded against
// the first, which is the loss PRD principle P6 is about. Treating it as free
// costs a copy the original's gate only once the original has become
// unopenable, at which point the original has lost it either way.
//
// A claimant that opens but whose assistant remote cannot be read is the
// deliberate opposite: the error is returned and the operation asking is
// refused. The two directions differ because the two states differ. A working
// copy that is gone or unopenable is one this package can say something about,
// and what it says is that nothing there points at the gate. A working copy
// that is standing there with a configuration that cannot be read is a fact
// this package failed to establish, and reading it as "nothing points at the
// gate" would hand a copy the original's gate on the strength of a read that
// did not happen.
func stillBound(ctx context.Context, set settings, claimant, repo string) (bool, error) {
	if !isDirectory(claimant) {
		return false, nil
	}
	resolved, err := resolvePath(claimant)
	if err != nil {
		return false, nil //nolint:nilerr // a claimant that will not resolve holds nothing
	}
	other, err := set.open(ctx, resolved)
	if err != nil {
		return false, nil //nolint:nilerr // a claimant that will not open holds nothing
	}
	named, err := boundRepository(ctx, other)
	if err != nil {
		return false, err
	}
	return named == repo, nil
}
