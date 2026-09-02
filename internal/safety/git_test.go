package safety_test

import (
	"context"
	"errors"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// repositoryWithCommitsNotIn is *vcs.Repository plus the one operation
// internal/vcs does not carry yet. safety.Git's doc comment claims that
// *vcs.Repository is the implementation this product uses and that CommitsNotIn
// is the only method it is missing; the assertion below is what makes both
// claims fail the build rather than rot quietly if a signature drifts.
//
// The shim supplies CommitsNotIn so the assertion holds without adding the
// operation to internal/vcs, which owns every git invocation and where adding it
// is the follow-up git.go names. Its body is never reached.
type repositoryWithCommitsNotIn struct {
	*vcs.Repository
}

func (repositoryWithCommitsNotIn) CommitsNotIn(context.Context, string, string) ([]string, error) {
	return nil, errors.ErrUnsupported
}

var _ safety.Git = repositoryWithCommitsNotIn{}
