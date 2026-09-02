package safety_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// repositoryWithCommitsNotIn is *vcs.Repository plus the one operation
// internal/vcs does not carry yet. safety.Git's doc comment claims that
// *vcs.Repository is the implementation this product uses; the assertion below
// makes that fail the build rather than rot quietly if one of the three
// signatures it does provide drifts.
//
// The shim supplies CommitsNotIn so the assertion holds without adding the
// operation to internal/vcs, which owns every git invocation and where adding it
// is the follow-up git.go names. Its body is never reached. Because an explicit
// method shadows a promoted one, this assertion alone would keep compiling after
// internal/vcs grew CommitsNotIn, which is what
// TestCommitsNotInIsStillTheNamedFollowUp covers.
type repositoryWithCommitsNotIn struct {
	*vcs.Repository
}

func (repositoryWithCommitsNotIn) CommitsNotIn(context.Context, string, string) ([]string, error) {
	return nil, errors.ErrUnsupported
}

var _ safety.Git = repositoryWithCommitsNotIn{}

func TestCommitsNotInIsStillTheNamedFollowUp(t *testing.T) {
	t.Parallel()
	// safety.Git's doc comment tells a reader that CommitsNotIn is the one
	// method internal/vcs does not carry yet. The compile-time assertion above
	// cannot notice the day that stops being true, because the shim's own
	// method shadows a promoted one, so this is what notices instead.
	var repository any = (*vcs.Repository)(nil)
	if _, ok := repository.(safety.Git); ok {
		t.Fatal("*vcs.Repository now satisfies safety.Git in full, so CommitsNotIn has landed in internal/vcs: git.go still calls it a pending follow-up and has to be corrected")
	}
}
