package vcs_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// pushSubject builds a bare repository standing in for a remote and a working
// copy that can push to it by path, with one commit already published. It
// returns the two paths and the published commit.
//
// It is built with git directly rather than through the package under test,
// because a fixture built with the code being tested cannot show that code
// wrong.
func pushSubject(t *testing.T) (remote, work, published string) {
	t.Helper()
	gitEnvironment(t)
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	work = filepath.Join(root, "work")
	rawGit(t, root, "init", "--quiet", "--bare", remote)
	rawGit(t, root, "init", "--quiet", work)
	writeFile(t, filepath.Join(work, "f.txt"), "one\n")
	rawGit(t, work, "add", "-A")
	rawGit(t, work, "commit", "--quiet", "-m", "one")
	published = strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))
	rawGit(t, work, "push", "--quiet", remote, "HEAD:refs/heads/main")
	return remote, work, published
}

// commitOn adds a commit to the working copy and returns it.
func commitOn(t *testing.T, work, content, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(work, "f.txt"), content)
	rawGit(t, work, "add", "-A")
	rawGit(t, work, "commit", "--quiet", "-m", message)
	return strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))
}

// remoteCommit reads what a reference names in the bare repository, directly.
func remoteCommit(t *testing.T, remote, ref string) string {
	t.Helper()
	return strings.TrimSpace(rawGit(t, remote, "rev-parse", ref))
}

func TestPushForwardsACommitUnderAHeldLease(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	next := commitOn(t, work, "two\n", "two")

	if err := repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/main",
		Commit: next,
		Lease:  vcs.Lease{Exists: true, Commit: published},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := remoteCommit(t, remote, "refs/heads/main"); got != next {
		t.Fatalf("refs/heads/main is %s, want %s", got, next)
	}
}

func TestPushRewritesHistoryUnderAHeldLease(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	// A commit that does not contain the published one, which is the shape an
	// anchored force update has: the update drops what the branch holds.
	rawGit(t, work, "checkout", "--quiet", "--detach", published+"~0")
	rawGit(t, work, "reset", "--quiet", "--hard", published)
	rawGit(t, work, "commit", "--quiet", "--allow-empty", "--amend", "-m", "rewritten")
	rewritten := strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))
	if rewritten == published {
		t.Fatal("the amend did not produce a different commit, so this checks nothing")
	}

	if err := repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/main",
		Commit: rewritten,
		Lease:  vcs.Lease{Exists: true, Commit: published},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := remoteCommit(t, remote, "refs/heads/main"); got != rewritten {
		t.Fatalf("refs/heads/main is %s, want %s", got, rewritten)
	}
}

func TestPushIsRejectedWhenTheLeaseNoLongerHolds(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	// Somebody else lands a commit on the branch, so the lease the caller
	// holds describes a state the reference has left.
	elsewhere := t.TempDir()
	rawGit(t, elsewhere, "clone", "--quiet", remote, "copy")
	copyPath := filepath.Join(elsewhere, "copy")
	rawGit(t, copyPath, "checkout", "--quiet", "main")
	landed := commitOn(t, copyPath, "theirs\n", "theirs")
	rawGit(t, copyPath, "push", "--quiet", "origin", "HEAD:refs/heads/main")

	ours := commitOn(t, work, "ours\n", "ours")
	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/main",
		Commit: ours,
		Lease:  vcs.Lease{Exists: true, Commit: published},
	})
	if !errors.Is(err, vcs.ErrPushRejected) {
		t.Fatalf("push error is %v, want one matching ErrPushRejected", err)
	}
	var rejection *vcs.PushRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("push error %v does not reach a *PushRejection", err)
	}
	if rejection.Ref != "refs/heads/main" {
		t.Fatalf("rejection names %q, want refs/heads/main", rejection.Ref)
	}
	if rejection.Reason == "" {
		t.Fatal("the rejection carries no reason, so a caller has nothing to report")
	}
	if got := remoteCommit(t, remote, "refs/heads/main"); got != landed {
		t.Fatalf("refs/heads/main is %s, want the commit somebody else landed, %s", got, landed)
	}
}

func TestPushCreatesAReferenceUnderALeaseOnAbsence(t *testing.T) {
	remote, work, _ := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	head := strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))

	if err := repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/fresh",
		Commit: head,
		Lease:  vcs.Lease{},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := remoteCommit(t, remote, "refs/heads/fresh"); got != head {
		t.Fatalf("refs/heads/fresh is %s, want %s", got, head)
	}
}

func TestPushIsRejectedWhenALeaseOnAbsenceMeetsAReferenceThatExists(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	next := commitOn(t, work, "two\n", "two")

	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/main",
		Commit: next,
		Lease:  vcs.Lease{},
	})
	if !errors.Is(err, vcs.ErrPushRejected) {
		t.Fatalf("push error is %v, want one matching ErrPushRejected", err)
	}
	if got := remoteCommit(t, remote, "refs/heads/main"); got != published {
		t.Fatalf("refs/heads/main is %s, want it left at %s", got, published)
	}
}

func TestPushRefusesALeaseThatDescribesNoState(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	head := strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))

	for _, tc := range []struct {
		name  string
		lease vcs.Lease
	}{
		{"present and naming nothing", vcs.Lease{Exists: true}},
		{"absent and naming a commit", vcs.Lease{Commit: published}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.Push(ctx(t), vcs.PushSpec{
				Remote: remote,
				Ref:    "refs/heads/main",
				Commit: head,
				Lease:  tc.lease,
			})
			if !errors.Is(err, vcs.ErrInvalidArgument) {
				t.Fatalf("push error is %v, want one matching ErrInvalidArgument", err)
			}
		})
	}
}

func TestPushRefusesAShortReferenceName(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	head := strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))

	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "main",
		Commit: head,
		Lease:  vcs.Lease{Exists: true, Commit: published},
	})
	if !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("push error is %v, want one matching ErrInvalidArgument", err)
	}
}

func TestPushRefusesARevisionThatDoesNotResolve(t *testing.T) {
	remote, work, published := pushSubject(t)
	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}

	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: remote,
		Ref:    "refs/heads/main",
		Commit: "no-such-revision",
		Lease:  vcs.Lease{Exists: true, Commit: published},
	})
	if !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("push error is %v, want one matching ErrRefNotFound", err)
	}
}

func TestCommitsNotInNamesWhatOneRevisionHoldsAndAnotherDoesNot(t *testing.T) {
	gitEnvironment(t)
	work := t.TempDir()
	rawGit(t, work, "init", "--quiet", ".")
	base := commitOn(t, work, "base\n", "base")
	rawGit(t, work, "checkout", "--quiet", "-b", "side")
	sideFirst := commitOn(t, work, "side one\n", "side one")
	sideSecond := commitOn(t, work, "side two\n", "side two")

	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	got, err := repo.CommitsNotIn(ctx(t), sideSecond, base)
	if err != nil {
		t.Fatalf("commits not in: %v", err)
	}
	want := []string{sideSecond, sideFirst}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v, most recent first", got, want)
		}
	}
}

func TestCommitsNotInIsEmptyWhenNothingWouldBeLost(t *testing.T) {
	gitEnvironment(t)
	work := t.TempDir()
	rawGit(t, work, "init", "--quiet", ".")
	base := commitOn(t, work, "base\n", "base")
	ahead := commitOn(t, work, "ahead\n", "ahead")

	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	got, err := repo.CommitsNotIn(ctx(t), base, ahead)
	if err != nil {
		t.Fatalf("commits not in: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want nothing: %s contains %s", got, ahead, base)
	}
}

func TestCommitsNotInRefusesARevisionThisRepositoryDoesNotHold(t *testing.T) {
	gitEnvironment(t)
	work := t.TempDir()
	rawGit(t, work, "init", "--quiet", ".")
	base := commitOn(t, work, "base\n", "base")

	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
	if _, err := repo.CommitsNotIn(ctx(t), "0000000000000000000000000000000000000000", base); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("error is %v, want one matching ErrRefNotFound", err)
	}
}
