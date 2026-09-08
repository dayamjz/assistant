package safety_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// safety.Git's doc comment claims that *vcs.Repository is the implementation
// this product uses. This is that claim as a compile-time assertion, so a
// signature that drifts on either side fails the build rather than rotting
// quietly into a sentence nobody rechecks.
//
// It is stated over *vcs.Repository directly and not over a type embedding it.
// An explicit method on a wrapper shadows a promoted one, so a wrapper would
// keep this compiling while covering for an operation internal/vcs had stopped
// providing, which is how the earlier form of this file had to carry a second
// test to notice what the assertion could not.
var _ safety.Git = (*vcs.Repository)(nil)

// TestTheGitInterfaceIsWhatVCSProvides fails when *vcs.Repository stops
// satisfying safety.Git, which the assertion above already fails the build on.
// It exists so a reader running the tests sees the claim checked rather than
// only compiled, and it names the file that has to be corrected on the day it
// stops holding.
func TestTheGitInterfaceIsWhatVCSProvides(t *testing.T) {
	t.Parallel()
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(safety.Git); !ok {
		t.Fatal("*vcs.Repository no longer satisfies safety.Git: git.go says it is the implementation this product uses and has to be corrected")
	}
}
