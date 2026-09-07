package gate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// WorkingCopyFor reports the working copy the gate with this identifier
// belongs to, as an absolute resolved path.
//
// It is the question a gate's hooks arrive with and cannot answer for
// themselves. A gate is filed under a hash of its working copy's path, so this
// package can find a gate from a path and cannot invert the hash to get the
// path back; what a hook carries is the identifier, and everything a push has
// to be validated against hangs off the working copy.
//
// The answer comes from the two sources held.claimants uses and on the same
// terms, so this is not a second owner of who a gate belongs to. The home's
// ownership index names every working copy recorded as bound, which is the
// enumerable answer. The gate's own record names the working copy the gate
// last belonged to, which survives the home's database being lost. Neither is
// believed on its own: a candidate counts only if it is still there and its
// own assistant remote still names this gate.
//
// It refuses rather than choosing. ErrNoGate says nothing under this
// identifier is a gate of this home. ErrGateUnbound says the gate is there and
// no working copy standing today points at it, which is what a push to a gate
// whose working copy has been deleted or detached meets. ErrInvalidSpec says
// the home or the identifier could not name a gate at all. A gate whose record
// will not read is ErrMalformedRecord, exactly as it is for every other
// operation here, because a record that cannot be read is a fact that cannot
// be established rather than one to work around.
//
// This is a read. It creates nothing, writes nothing, and unlike the seam
// every operation that changes a gate goes through, it seals no gate it looks
// at: a caller that only asked whose a gate is has not put a gate into a state
// that needs closing.
func WorkingCopyFor(ctx context.Context, home, id string, opts ...Option) (string, error) {
	set := resolveSettings(opts)
	if set.index == nil {
		return "", fmt.Errorf("%w: which working copy a gate belongs to is recorded in the home's index, "+
			"and this cannot be answered without one; pass gate.WithIndex with the store this home is "+
			"opened against", ErrNoIndex)
	}
	root, err := validateHome(home)
	if err != nil {
		return "", err
	}
	if !isIdentifier(id) {
		return "", fmt.Errorf("%w: %q is not a gate identifier, which is %d lowercase hexadecimal digits",
			ErrInvalidSpec, id, identifierLength)
	}
	repo := repositoryPath(root, id)
	rec, holds, err := gateRepository(root, repo)
	if err != nil {
		return "", err
	}
	if holds == noRepository {
		return "", fmt.Errorf("%w: %s holds no gate repository; initialize the working copy this gate "+
			"belongs to, which creates it", ErrNoGate, repo)
	}

	candidates := make([]string, 0, 2)
	if holds == repositoryWithRecord {
		candidates = append(candidates, rec.WorkingPath)
	}
	bindings, err := set.index.GateBindings(ctx, id)
	if err != nil {
		return "", fmt.Errorf("gate: reading which working copies are bound to gate %s: %w", id, err)
	}
	for _, binding := range bindings {
		candidates = append(candidates, binding.WorkingPath)
	}

	seen := make(map[string]struct{}, len(candidates))
	var holders []string
	for _, candidate := range candidates {
		if _, asked := seen[candidate]; asked {
			continue
		}
		seen[candidate] = struct{}{}
		bound, err := stillBound(ctx, set, candidate, repo)
		if err != nil {
			return "", err
		}
		if bound {
			holders = append(holders, candidate)
		}
	}
	switch len(holders) {
	case 1:
		return holders[0], nil
	case 0:
		return "", fmt.Errorf("%w: no working copy standing today has an %s remote naming %s; "+
			"initialize the working copy this gate belongs to, which points it back at the gate",
			ErrGateUnbound, RemoteName, repo)
	default:
		// Refusing is the whole of the answer here. Which of several working
		// copies a push was meant for is not something this package can read
		// out of the gate, and picking one would attach a run to a repository
		// nobody named.
		return "", fmt.Errorf("%w: %s is named by more than one working copy standing today, %s, so "+
			"which one a push belongs to is not established; remove the %s remote from the ones that "+
			"should not have it", ErrGateUnbound, repo, strings.Join(holders, ", "), RemoteName)
	}
}

// validateHome is the home half of validatePaths, for the operations that take
// a home and no working path. It is shared so that what counts as a home is
// one rule rather than one per entry point.
func validateHome(home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("%w: home is empty", ErrInvalidSpec)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("%w: home %q is not absolute", ErrInvalidSpec, home)
	}
	return resolveExisting(home), nil
}

// isIdentifier reports whether id is written the way Identify writes one.
//
// It is checked rather than assumed because an identifier reaches this package
// from a hook's command line, and repositoryPath joins it onto the home's
// repository directory: a value carrying a separator or a parent reference
// would name a path outside that directory. gateRepository refuses such a path
// as well, so this is the first of two rather than the only one, and it is
// here so that the refusal names the identifier the caller wrote rather than
// the path it would have built.
func isIdentifier(id string) bool {
	if len(id) != identifierLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
