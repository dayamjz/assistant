package safety

import (
	"context"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Git is the git mechanism this package's policy runs on. It is an interface
// rather than a *vcs.Repository so that a decision can be tested against a
// history a test states outright, and so that the mechanism stays replaceable;
// *vcs.Repository is the implementation this product uses.
//
// Every method is a question, and none of them moves a reference. This package
// decides whether an update may proceed; performing it is the caller's job,
// and nothing here can perform one on the caller's behalf.
//
// ResolveCommit, MergeBase, and CommitsNotIn are answered from the local
// repository, so a commit that only exists on the remote is not answerable
// until the caller has fetched it. That is the intended shape: an unanswerable
// question becomes a refusal, never an allow.
//
// CommitsNotIn is the one method *vcs.Repository does not implement today.
// This package does not add it, because internal/vcs is the only package that
// invokes git and adding an operation there is not this package's change to
// make. Until it exists, the typed operation vcs needs is the one declared
// here: the commits reachable from one revision and not from another, which is
// git rev-list incorporated..have. This is a named follow-up, not an oversight.
type Git interface {
	// RemoteRefs reads the references a remote advertises, without changing
	// anything locally. This is the fresh read every decision is made
	// against.
	RemoteRefs(ctx context.Context, remote string, patterns ...string) ([]vcs.Ref, error)
	// ResolveCommit resolves a revision to a full commit identifier in the
	// local repository.
	ResolveCommit(ctx context.Context, rev string) (string, error)
	// MergeBase returns the best common ancestor of two commits, and an error
	// matching vcs.ErrNoMergeBase when they share none.
	MergeBase(ctx context.Context, a, b string) (string, error)
	// CommitsNotIn returns the commits reachable from have and not from
	// incorporated, most recent first. An empty result means incorporated
	// already contains everything have does, so replacing have with
	// incorporated loses no commit.
	CommitsNotIn(ctx context.Context, have, incorporated string) ([]string, error)
}
