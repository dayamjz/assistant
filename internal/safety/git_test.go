package safety_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// safety.Git's doc comment claims that *vcs.Repository is the implementation
// this product uses, and now that CommitsNotIn has landed there the claim is
// whole: every method of the interface is one that type provides. The
// assertion makes a signature drifting on either side fail the build rather
// than rot quietly, and no shim stands between the two, so nothing here can
// shadow a method the repository stopped providing.
var _ safety.Git = (*vcs.Repository)(nil)

func TestTheRepositoryIsTheGitMechanismInFull(t *testing.T) {
	t.Parallel()
	// The assertion above is what fails the build, and it is checked at
	// compile time whether or not any test runs. This asks the same question
	// of a value rather than of a declaration, so a reader running the suite
	// sees the claim answered rather than having to know that an unused
	// variable was doing the work.
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(safety.Git); !ok {
		t.Fatal("*vcs.Repository no longer satisfies safety.Git: git.go says it is the implementation this product uses, so either the interface or that sentence has to change")
	}
}
