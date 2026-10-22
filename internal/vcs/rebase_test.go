package vcs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestRebaseStatusDistinguishesAnActiveRebaseFromAnAbortedOne checks the
// status operation against a real conflicting replay and its abort. A clean
// copy must return false without treating the missing REBASE_HEAD as failure.
func TestRebaseStatusDistinguishesAnActiveRebaseFromAnAbortedOne(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	base := commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("before rebase: active %v, error %v; want false without error", active, err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing %s onto %s from %s: %v, want a conflict", topic, main, base, err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("during rebase: active %v, error %v; want true without error", active, err)
	}
	if err := repo.RebaseAbort(ctx(t)); err != nil {
		t.Fatalf("aborting the rebase: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after abort: active %v, error %v; want false without error", active, err)
	}
	if head, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil || head != topic {
		t.Fatalf("after abort HEAD = %s, %v; want the original %s", head, err, topic)
	}
}

// TestRebaseStatusSeesARebasePausedBeforeAnyCommitIsPicked is the case
// REBASE_HEAD does not cover. An interactive rebase stopped at an initial
// break has no such name, the rebase-merge directory is present, and abort
// succeeds. Status has to report that rebase as in progress.
func TestRebaseStatusSeesARebasePausedBeforeAnyCommitIsPicked(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	base := commitFile(t, dir, "note.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, dir, "note.txt", "topic\n", "topic")
	script := filepath.Join(t.TempDir(), "edit.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'break\\n' > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "rebase", "-i", base)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_SEQUENCE_EDITOR=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "GIT_SEQUENCE_EDITOR="+script)
	out, err := cmd.CombinedOutput()
	repo, openErr := vcs.OpenWorktree(ctx(t), dir)
	if openErr != nil {
		t.Fatal(openErr)
	}
	active, statusErr := repo.RebaseStatus(ctx(t))
	if statusErr != nil || !active {
		t.Fatalf("paused rebase: active %v, status error %v, command error %v\n%s", active, statusErr, err, out)
	}
	if _, missing := tryRawGit(dir, "rev-parse", "--verify", "--quiet", "REBASE_HEAD"); missing == nil {
		t.Fatal("REBASE_HEAD resolved during a break before any commit was picked")
	}
	if err := repo.RebaseAbort(ctx(t)); err != nil {
		t.Fatalf("aborting the paused rebase: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after abort: active %v, error %v; want false without error", active, err)
	}
	if head, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil || head != topic {
		t.Fatalf("after abort HEAD = %s, %v; want the original %s", head, err, topic)
	}
}

// TestRebaseContinueCommitsTheResolutionOntoTheNewBase checks that a
// conflicted replay, once its resolution is committed, continues onto the
// commit the rebase had checked out. Continuing while the conflict is still
// unmerged must fail and leave the rebase in progress.
func TestRebaseContinueCommitsTheResolutionOntoTheNewBase(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	first, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.UncommittedDigest(ctx(t))
	if err != nil || second != first {
		t.Fatalf("digest changed with no edit: %q then %q, error %v", first, second, err)
	}
	if err := repo.RebaseContinue(ctx(t)); err == nil {
		t.Fatal("continuing with the conflict still unmerged succeeded")
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("after a refused continue: active %v, error %v; want the rebase still in progress", active, err)
	}
	writeFile(t, filepath.Join(dir, "clash.txt"), "topic and main\n")
	edited, err := repo.UncommittedDigest(ctx(t))
	if err != nil || edited == first {
		t.Fatalf("digest after the edit = %q, before %q, error %v; want a different digest", edited, first, err)
	}
	committed, err := repo.CommitAll(ctx(t), "resolve the conflict")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RebaseContinue(ctx(t)); err != nil {
		t.Fatalf("continuing after the resolution was committed: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after continue: active %v, error %v; want false without error", active, err)
	}
	head, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if head != committed {
		t.Fatalf("HEAD = %s, want the resolution %s", head, committed)
	}
	ancestor, err := repo.IsAncestor(ctx(t), main, head)
	if err != nil || !ancestor {
		t.Fatalf("main %s ancestor of %s: %v, %v", main, head, ancestor, err)
	}
}

// TestRebaseSkipDropsAnEmptyStepAndKeepsAnEarlierCommit is a conflict whose
// resolution matches the commit the rebase stopped on. Skipping that step
// finishes the rebase and keeps the commit that already applied. The step
// itself is not committed first.
func TestRebaseSkipDropsAnEmptyStepAndKeepsAnEarlierCommit(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "keep.txt", "topic-keep\n", "clean")
	commitFile(t, dir, "clash.txt", "topic\n", "conflict")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	writeFile(t, filepath.Join(dir, "clash.txt"), "main\n")
	if _, err := repo.CommitAll(ctx(t), "resolve"); !errors.Is(err, vcs.ErrNothingToCommit) {
		t.Fatalf("committing a resolution that matches HEAD: %v, want nothing to commit", err)
	}
	if err := repo.RebaseSkip(ctx(t)); err != nil {
		t.Fatalf("skipping the empty step: %v", err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || active {
		t.Fatalf("after skip: active %v, error %v; want the rebase finished", active, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "keep.txt"))
	if err != nil || string(body) != "topic-keep\n" {
		t.Fatalf("keep.txt = %q, error %v", body, err)
	}
	head, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	ancestor, err := repo.IsAncestor(ctx(t), main, head)
	if err != nil || !ancestor {
		t.Fatalf("main %s ancestor of %s: %v, %v", main, head, ancestor, err)
	}
}

// TestUncommittedDigestIgnoresATextconvThatCollapsesBytes is the trust
// boundary on the digest a rebase fix round compares. A .gitattributes entry
// can name a textconv, and a textconv that prints one text for every blob
// makes git diff report no change. A real edit still has to change the digest,
// and the textconv program must not run to decide that.
func TestUncommittedDigestIgnoresATextconvThatCollapsesBytes(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "flat.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'x\\n' >> '"+marker+"'\nprintf 'flat\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	commitFile(t, dir, ".gitattributes", "*.txt diff=flat\n", "attributes")
	commitFile(t, dir, "note.txt", "one\n", "one")
	rawGit(t, dir, "config", "diff.flat.textconv", script)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "note.txt"), "two\n")
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("a real edit left the digest unchanged")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the textconv program ran while the digest was read")
	}
}

// TestUncommittedDigestReadsALargeChangeWithoutThePatch is a blob whose
// literal delta does not fit in the invocation output cap. The digest has to
// come back anyway, and a further edit has to change it. Reading the patch
// fails before a fix round can ask the fixer.
func TestUncommittedDigestReadsALargeChangeWithoutThePatch(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	body := strings.Repeat("a", 4096)
	commitFile(t, dir, "blob.bin", body, "blob")
	repo, err := vcs.OpenWorktree(ctx(t), dir, vcs.WithMaxOutput(64))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "blob.bin"), strings.Repeat("b", 4096))
	first, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatalf("digest of a change larger than the output cap: %v", err)
	}
	writeFile(t, filepath.Join(dir, "blob.bin"), strings.Repeat("c", 4096))
	second, err := repo.UncommittedDigest(ctx(t))
	if err != nil || second == first {
		t.Fatalf("digest after a further edit = %q, before %q, error %v", second, first, err)
	}
}

// TestUncommittedDigestSeesAnIndexResolutionThatDoesNotChangeBytes is a
// modify/delete conflict whose worktree already holds the bytes the resolution
// keeps. Staging that file does not change the worktree, and the digest has to
// change anyway, because the index is what a later commit records.
func TestUncommittedDigestSeesAnIndexResolutionThatDoesNotChangeBytes(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "keep.txt", "topic-keep\n", "clean")
	rawGit(t, dir, "rm", "--quiet", "clash.txt")
	rawGit(t, dir, "commit", "--quiet", "-m", "delete clash")
	rawGit(t, dir, "checkout", "--quiet", "main")
	commitFile(t, dir, "clash.txt", "main\n", "modify clash")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: "main"}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing: %v, want a modify/delete conflict", err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "clash.txt")
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil || after == before {
		t.Fatalf("digest after staging the file left in the tree = %q, before %q, error %v", after, before, err)
	}
}

// TestUncommittedDigestSeesAModeOnlyChangeOnAConflictedPath is a conflicted
// file whose bytes stay put. Changing its mode has to change the digest. The
// path is already listed, so a record of bytes alone does not.
func TestUncommittedDigestSeesAModeOnlyChangeOnAConflictedPath(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "clash.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil || after == before {
		t.Fatalf("digest after chmod = %q, before %q, error %v", after, before, err)
	}
}

// TestUncommittedDigestSeesASubmoduleCheckoutMove is a submodule whose
// checkout is already a difference from the index. Moving that checkout to
// another commit has to change the digest. A directory recorded with no
// commit does not.
func TestUncommittedDigestSeesASubmoduleCheckoutMove(t *testing.T) {
	gitEnvironment(t)
	sub := t.TempDir()
	rawGit(t, sub, "init", "--quiet", "-b", "main", ".")
	one := commitFile(t, sub, "f.txt", "one\n", "one")
	two := commitFile(t, sub, "f.txt", "two\n", "two")
	three := commitFile(t, sub, "f.txt", "three\n", "three")
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	rawGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "lib")
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", one)
	rawGit(t, dir, "add", "lib")
	rawGit(t, dir, "commit", "--quiet", "-m", "base")
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", two)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", three)
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil || after == before {
		t.Fatalf("digest after moving the submodule from %s to %s = %q, before %q, error %v", two, three, after, before, err)
	}
}

// divergentSubmodule is a superproject stopped on a submodule conflict. The
// checkout is attached to the submodule's main at topic, which is one side of
// the conflict. fix is another commit in that submodule, on neither side.
func divergentSubmodule(t *testing.T) (dir, fix string) {
	t.Helper()
	sub := t.TempDir()
	rawGit(t, sub, "init", "--quiet", "-b", "main", ".")
	base := commitFile(t, sub, "f.txt", "base\n", "base")
	rawGit(t, sub, "checkout", "--quiet", "-b", "topic")
	topic := commitFile(t, sub, "f.txt", "topic\n", "topic")
	rawGit(t, sub, "checkout", "--quiet", "main")
	upstream := commitFile(t, sub, "f.txt", "upstream\n", "upstream")
	rawGit(t, sub, "checkout", "--quiet", "-b", "fix")
	fix = commitFile(t, sub, "f.txt", "fix\n", "fix")
	rawGit(t, sub, "checkout", "--quiet", "main")
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	rawGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "lib")
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", base)
	rawGit(t, dir, "add", "lib")
	rawGit(t, dir, "commit", "--quiet", "-m", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", topic)
	rawGit(t, dir, "add", "lib")
	rawGit(t, dir, "commit", "--quiet", "-m", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", upstream)
	rawGit(t, dir, "add", "lib")
	rawGit(t, dir, "commit", "--quiet", "-m", "upstream")
	main := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "checkout", "--quiet", "topic")
	if err := func() error {
		repo, err := vcs.OpenWorktree(ctx(t), dir)
		if err != nil {
			return err
		}
		return repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main})
	}(); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a submodule conflict", main, err)
	}
	rawGit(t, dir, "-C", "lib", "checkout", "--quiet", "-B", "main", topic)
	return dir, fix
}

// TestUncommittedDigestFollowsASubmoduleSymbolicRef is a conflicted submodule
// whose HEAD points through refs/heads/alias at refs/heads/main. Moving main
// between two commits that both differ from the index has to change the
// digest. Recording the alias text does not.
func TestUncommittedDigestFollowsASubmoduleSymbolicRef(t *testing.T) {
	gitEnvironment(t)
	dir, fix := divergentSubmodule(t)
	rawGit(t, dir, "-C", "lib", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	rawGit(t, dir, "-C", "lib", "symbolic-ref", "HEAD", "refs/heads/alias")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "-C", "lib", "update-ref", "refs/heads/main", fix)
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil || after == before {
		t.Fatalf("digest after moving the symbolic target to %s = %q, before %q, error %v", fix, after, before, err)
	}
}

// TestUncommittedDigestReadsAReftableSubmoduleHead is a conflicted submodule
// whose checkout stores refs in a reftable, with HEAD attached. The digest
// has to resolve that HEAD, and moving the branch has to change it. Opening
// the sentinel path reftable writes is a filesystem error, and the round
// would fail before the fixer ran.
func TestUncommittedDigestReadsAReftableSubmoduleHead(t *testing.T) {
	gitEnvironment(t)
	dir, fix := divergentSubmodule(t)
	rawGit(t, dir, "-C", "lib", "refs", "migrate", "--ref-format=reftable")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.UncommittedDigest(ctx(t))
	if err != nil {
		t.Fatalf("digest of a reftable submodule: %v", err)
	}
	rawGit(t, dir, "-C", "lib", "update-ref", "refs/heads/main", fix)
	after, err := repo.UncommittedDigest(ctx(t))
	if err != nil || after == before {
		t.Fatalf("digest after moving the reftable branch to %s = %q, before %q, error %v", fix, after, before, err)
	}
}
