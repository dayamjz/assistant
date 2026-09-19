package safety_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// safety.Git's doc comment claims that *vcs.Repository is the implementation
// this product uses and that it satisfies the interface in full. This is that
// claim as a compile-time assertion, so a signature drifting on either side
// fails the build rather than rotting quietly into a comment nobody checks.
//
// It replaces a shim that supplied CommitsNotIn, from when internal/vcs did
// not carry that operation, together with the test that watched for the day it
// landed. That day came: the shim would now shadow the real method and keep
// compiling whatever internal/vcs did, so the shim is what had to go.
var _ safety.Git = (*vcs.Repository)(nil)

// TestTheProductsGitIsTheOneThisPackageDecidesOn is the assertion above in a
// form that runs, so a reader looking for the claim in the test output finds it
// stated rather than having to know that a compile-time assertion is what
// checks it.
//
// It is a positive control on that assertion and not a second copy of it: the
// interface conversion here is the one a caller outside this package makes, so
// a build where *vcs.Repository stopped satisfying safety.Git fails here with a
// message rather than only at the var above.
func TestTheProductsGitIsTheOneThisPackageDecidesOn(t *testing.T) {
	t.Parallel()
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(safety.Git); !ok {
		t.Fatal("*vcs.Repository no longer satisfies safety.Git, so the mechanism this package " +
			"decides on is not the one the product pushes with: git.go's claim has to be corrected " +
			"or the missing operation restored to internal/vcs")
	}
}
