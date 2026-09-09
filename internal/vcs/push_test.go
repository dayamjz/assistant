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

// An operator's git configuration must not turn one PushSpec into two
// reference updates. push.followTags makes git send every annotated tag
// reachable from the commit being pushed, and this package keeps
// GIT_CONFIG_GLOBAL on purpose, so that setting reaches the invocation.
//
// A tag created that way is a reference on the shared remote that no lease
// covered and that internal/safety never decided on, which is what makes this
// P6's problem rather than a tidiness one.
func TestPushDoesNotCarryATagAConfigurationWouldFollow(t *testing.T) {
	cfg := gitEnvironment(t)
	appendConfig(t, cfg, "[push]\n\tfollowTags = true\n")
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	rawGit(t, root, "init", "--quiet", "--bare", remote)
	rawGit(t, root, "init", "--quiet", work)
	writeFile(t, filepath.Join(work, "f.txt"), "one\n")
	rawGit(t, work, "add", "-A")
	rawGit(t, work, "commit", "--quiet", "-m", "one")
	published := strings.TrimSpace(rawGit(t, work, "rev-parse", "HEAD"))
	rawGit(t, work, "push", "--quiet", remote, "HEAD:refs/heads/main")

	next := commitOn(t, work, "two\n", "two")
	// An annotated tag reachable from the commit being pushed, which is what
	// push.followTags sends alongside it. A lightweight tag is not followed,
	// so this has to be annotated for the setting to have anything to do.
	rawGit(t, work, "tag", "--annotate", "-m", "release one", "v1", next)

	repo, err := vcs.OpenWorktree(ctx(t), work)
	if err != nil {
		t.Fatalf("open %s: %v", work, err)
	}
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
	refs := strings.TrimSpace(rawGit(t, remote, "for-each-ref", "--format=%(refname)"))
	if refs != "refs/heads/main" {
		t.Fatalf("the remote holds %q, want refs/heads/main and nothing else: one PushSpec "+
			"updated a reference no lease covered", refs)
	}
}

// The fate of the reference a caller asked to update is that reference's own
// status line, never another's. git exits non-zero when it refused anything at
// all, so a push whose branch landed while some other reference was refused
// exits 1 with both outcomes in its output, and reading the first rejection
// would report a shared branch as untouched when it had in fact moved.
//
// The output below is what real git printed for exactly that case: a receiving
// repository with an update hook declining refs/tags/*, and push.followTags
// sending a tag along with the branch. The stand-in replays those bytes,
// because with --no-follow-tags now pinned this package can no longer be made
// to produce a second reference itself, while a push option the receiving side
// acts on still can.
func TestPushReportsItsOwnReferencesFateWhenAnotherIsRejected(t *testing.T) {
	logPath, exe := useFakeGit(t)
	fakeGitOutput(t, "To /somewhere/remote.git\n"+
		" \t"+fakeCommit+":refs/heads/main\t65daf36..5dbd1fc\n"+
		"!\trefs/tags/v1:refs/tags/v1\t[remote rejected] (hook declined)\n"+
		"Done\n")
	fakeGitStderrOutput(t, "remote: error: hook declined to update refs/tags/v1\n", 1)

	repo, err := vcs.OpenWorktree(ctx(t), t.TempDir(), vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenWorktree against the stand-in git: %v", err)
	}
	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: "origin",
		Ref:    "refs/heads/main",
		Commit: fakeCommit,
		Lease:  vcs.Lease{Exists: true, Commit: fakeCommit},
	})
	if err != nil {
		t.Fatalf("push reported %v, want success: refs/heads/main moved and only refs/tags/v1 "+
			"was refused, so reporting a rejection says a branch did not move when it did", err)
	}
	// The invocation is read as well, so this cannot pass because no push was
	// attempted at all.
	if !pushWasInvoked(t, logPath) {
		t.Fatal("the stand-in git recorded no push, so nothing about a push was checked")
	}
}

// A push whose own reference was refused is still a rejection, which is what
// keeps the test above from passing by never reporting one.
func TestPushReportsARejectionOfItsOwnReferenceAlongsideAnothers(t *testing.T) {
	logPath, exe := useFakeGit(t)
	fakeGitOutput(t, "To /somewhere/remote.git\n"+
		"!\t"+fakeCommit+":refs/heads/main\t[rejected] (stale info)\n"+
		"!\trefs/tags/v1:refs/tags/v1\t[remote rejected] (hook declined)\n"+
		"Done\n")
	fakeGitStderrOutput(t, "", 1)

	repo, err := vcs.OpenWorktree(ctx(t), t.TempDir(), vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenWorktree against the stand-in git: %v", err)
	}
	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: "origin",
		Ref:    "refs/heads/main",
		Commit: fakeCommit,
		Lease:  vcs.Lease{Exists: true, Commit: fakeCommit},
	})
	var rejection *vcs.PushRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("push error %v does not reach a *PushRejection", err)
	}
	if rejection.Ref != "refs/heads/main" {
		t.Fatalf("rejection names %q, want refs/heads/main", rejection.Ref)
	}
	if !strings.Contains(rejection.Reason, "stale info") {
		t.Fatalf("rejection reason is %q, want refs/heads/main's own summary rather than "+
			"another reference's", rejection.Reason)
	}
	if !pushWasInvoked(t, logPath) {
		t.Fatal("the stand-in git recorded no push, so nothing about a push was checked")
	}
}

// Output that says nothing about the reference the caller asked to update
// leaves what happened to it unknown, and this package reports that rather
// than reading silence as a push that happened.
func TestPushRefusesOutputThatSaysNothingAboutItsOwnReference(t *testing.T) {
	logPath, exe := useFakeGit(t)
	fakeGitOutput(t, "To /somewhere/remote.git\n"+
		"!\trefs/tags/v1:refs/tags/v1\t[remote rejected] (hook declined)\n"+
		"Done\n")
	fakeGitStderrOutput(t, "", 1)

	repo, err := vcs.OpenWorktree(ctx(t), t.TempDir(), vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenWorktree against the stand-in git: %v", err)
	}
	err = repo.Push(ctx(t), vcs.PushSpec{
		Remote: "origin",
		Ref:    "refs/heads/main",
		Commit: fakeCommit,
		Lease:  vcs.Lease{Exists: true, Commit: fakeCommit},
	})
	if err == nil {
		t.Fatal("push reported success on output naming no status for refs/heads/main, which " +
			"reads silence about a reference as evidence that it moved")
	}
	if errors.Is(err, vcs.ErrPushRejected) {
		t.Fatalf("push reported %v, which reports another reference's rejection as this one's", err)
	}
	// The invocation is read as well, like both siblings read it: an error
	// from a refusal before git ran would otherwise satisfy the two checks
	// above with the silence branch never reached.
	if !pushWasInvoked(t, logPath) {
		t.Fatal("the stand-in git recorded no push, so nothing about a push was checked")
	}
}

// pushWasInvoked reports whether the stand-in git was actually asked to push.
func pushWasInvoked(t *testing.T, logPath string) bool {
	t.Helper()
	for _, call := range readInvocations(t, logPath) {
		for _, arg := range call.Args {
			if arg == "push" {
				return true
			}
		}
	}
	return false
}
