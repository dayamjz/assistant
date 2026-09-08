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
// *vcs.Repository provides all four, so it satisfies this interface without
// anything standing between the two. A compile-time assertion in this
// package's tests fails the build if either side drifts, which is what keeps
// the sentence above from rotting into a claim nobody checks.
type Git interface {
	// RemoteRefs reads the references a remote advertises, without changing
	// anything locally. This is the fresh read every decision is made
	// against. Patterns narrow what comes back the way git ls-remote narrows
	// it, and no pattern means every reference.
	//
	// Ref.Object has to be the object a reference names, and Ref.Commit the
	// object it peels to whenever the read asked for the peeled name. This
	// package needs those two apart, because the lease it hands back is
	// compared against the object the reference names while its reachability
	// comparisons are answered on the object that reference peels to, and a
	// reference where the two differ is one it refuses rather than decides
	// about. What it does to get them apart is ask for the reference name and
	// its ^{} form together, so an implementation that reports a peeled
	// object only when the read named it satisfies this. An implementation
	// that reports Commit equal to Object for a reference peeling elsewhere,
	// with the peeled name asked for, removes that refusal without anything
	// here being able to tell.
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
