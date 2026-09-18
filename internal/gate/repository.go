package gate

import (
	"fmt"
)

// RepositoryFor reports the absolute path of the bare repository the gate with
// this identifier is held in.
//
// Where a gate's repository lives is this package's fact - PRD section 8 puts
// it at repos/<id>.git beneath the home, and this package is the one place
// that composes it, so internal/home does not. A caller that has to open that
// repository asks rather than joining the path for itself, because a second
// composition of it is a second owner of where a gate lives.
//
// The one caller today is internal/service, which cuts a run's isolated copy
// as a linked worktree of the gate the run's repository pushed through. That
// copy is the run's alone; the repository it is cut from is shared by every
// run of that repository, which is what makes this a read of long-lived state
// rather than a handle on something the caller may reshape.
//
// It refuses rather than composing blindly. ErrInvalidSpec says the home or
// the identifier could not name a gate at all, ErrNotAGate says the path they
// name is not one this home files a gate at, and ErrNoGate says nothing under
// this identifier is a gate of this home. A record that will not read is
// ErrMalformedRecord, as it is for every other operation here.
//
// # What it does not establish
//
// It answers where a gate is and nothing else. It does not establish that the
// gate belongs to any particular working copy, and it weighs no evidence of
// ownership, so it is not the question WorkingCopyFor answers and may not be
// used in place of it. A caller acting on a run has settled which repository
// that run is of before it reaches here, through the record PRD section 8
// makes authoritative for it, and this read neither checks that nor stands in
// for it.
//
// Like WorkingCopyFor this is a read: it creates nothing, writes nothing, and
// seals no gate it looks at. Unlike WorkingCopyFor, what the caller then does
// with the path may write - a worktree added to a repository is a change to
// it - and no seam here mediates that. This returns a path, and what is done
// with it belongs to whoever asked.
func RepositoryFor(home, id string) (string, error) {
	root, err := validateHome(home)
	if err != nil {
		return "", err
	}
	if !isIdentifier(id) {
		return "", fmt.Errorf("%w: %q is not a gate identifier, which is %d lowercase hexadecimal digits",
			ErrInvalidSpec, id, identifierLength)
	}
	repo := repositoryPath(root, id)
	_, holds, err := gateRepository(root, repo)
	if err != nil {
		return "", err
	}
	if holds == noRepository {
		return "", fmt.Errorf("%w: %s holds no gate repository; initialize the working copy this gate "+
			"belongs to, which creates it", ErrNoGate, repo)
	}
	return repo, nil
}
