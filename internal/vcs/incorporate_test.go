package vcs_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// twelveLines is a file whose lines are far enough apart that an edit at
// line 2 and an edit at line 11 integrate without touching each other's
// context.
const twelveLines = "one\nsame\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\nsame\ntwelve\n"

// incorporationRepo builds a working copy on main with twelveLines at its
// base commit and a feature branch of two commits on top of it: the first
// edits line 2, the second edits line 7. HEAD is left on main at the base.
func incorporationRepo(t *testing.T) (dir, base, first, second string) {
	t.Helper()
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "f.txt"), twelveLines)
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "base")
	base = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	rawGit(t, dir, "switch", "--quiet", "--create", "feature")
	writeFile(t, filepath.Join(dir, "f.txt"), strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1))
	rawGit(t, dir, "commit", "--quiet", "-am", "edit line two")
	first = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(dir, "f.txt"), strings.Replace(
		strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "seven\n", "SEVEN\n", 1))
	rawGit(t, dir, "commit", "--quiet", "-am", "edit line seven")
	second = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "switch", "--quiet", "main")
	return dir, base, first, second
}

// A branch squashed into the default branch reads as incorporated, by its tip
// and by each of its commits, whether the tip is named by reference or by
// identifier. One more commit on the branch that the squash does not carry
// reads false.
func TestIncorporatedRecognizesASquashAndNotAnExtraCommit(t *testing.T) {
	gitEnvironment(t)
	dir, _, first, second := incorporationRepo(t)
	rawGit(t, dir, "merge", "--quiet", "--squash", "feature")
	rawGit(t, dir, "commit", "--quiet", "-m", "feature, squashed")
	squashed := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, into := range []string{"main", squashed} {
		got, err := repo.Incorporated(ctx(t), []string{"feature"}, into)
		if err != nil || !got {
			t.Errorf("Incorporated(feature, %s) = %v, %v; want true", into, got, err)
		}
	}
	if got, err := repo.Incorporated(ctx(t), []string{first, second}, "main"); err != nil || !got {
		t.Errorf("Incorporated(each commit, main) = %v, %v; want true", got, err)
	}

	rawGit(t, dir, "switch", "--quiet", "feature")
	writeFile(t, filepath.Join(dir, "extra.txt"), "not in the squash\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "extra")
	rawGit(t, dir, "switch", "--quiet", "main")
	if got, err := repo.Incorporated(ctx(t), []string{"feature"}, "main"); err != nil || got {
		t.Errorf("Incorporated(feature with an extra commit, main) = %v, %v; want false with no error", got, err)
	}
	// The commits the squash does carry still read true beside it.
	if got, err := repo.Incorporated(ctx(t), []string{second}, "main"); err != nil || !got {
		t.Errorf("Incorporated(second, main) = %v, %v; want true", got, err)
	}
}

// A conflict between a commit and the tip is not an answer. It is the typed
// error, carrying its cause, and never a clean false: PRD principle P12 says
// anything inconclusive refuses, and this is the mechanism's half of that.
func TestIncorporatedReportsAConflictAsInconclusive(t *testing.T) {
	principles.Cite(t, principles.P12)
	gitEnvironment(t)
	dir, _, first, _ := incorporationRepo(t)
	// main edits the same line the feature's first commit edits, differently.
	writeFile(t, filepath.Join(dir, "f.txt"), strings.Replace(twelveLines, "one\nsame\n", "one\nconflicting\n", 1))
	rawGit(t, dir, "commit", "--quiet", "-am", "conflicting edit of line two")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.Incorporated(ctx(t), []string{first}, "main")
	if err == nil {
		t.Fatalf("Incorporated over a conflict = %v with no error, want ErrInconclusive", got)
	}
	if !errors.Is(err, vcs.ErrInconclusive) || !errors.Is(err, vcs.ErrMergeConflict) {
		t.Errorf("Incorporated over a conflict: %v, want ErrInconclusive wrapping ErrMergeConflict", err)
	}
	if got {
		t.Errorf("Incorporated over a conflict reported true beside its error")
	}
	// Nothing was written: the copy is still on main, clean, at the same tip.
	if status := rawGit(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the working copy was written during the check:\n%s", status)
	}
}

// The case PRD section 9 says patch identity gets wrong: the same hunk applied
// elsewhere in the file is a different change. The file carries one window of
// lines twice, so an edit at either copy produces a hunk with the same text
// and the same context and the two commits are shown to have equal patch
// identifiers first; the test is then about the case it names, and
// Incorporated still reads false.
func TestIncorporatedDistinguishesAnIdenticalHunkElsewhere(t *testing.T) {
	principles.Cite(t, principles.P12)
	gitEnvironment(t)
	const window = "x\ny\nz\nsame\nw\nv\nu\n"
	const padding = "pad one\npad two\npad three\npad four\n"
	const base = window + padding + window
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "g.txt"), base)
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "base")

	// The feature edits the first copy of the window.
	rawGit(t, dir, "switch", "--quiet", "--create", "feature")
	writeFile(t, filepath.Join(dir, "g.txt"), strings.Replace(base, "same", "changed", 1))
	rawGit(t, dir, "commit", "--quiet", "-am", "edit the first window")
	first := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	// main edits the second copy, identically.
	rawGit(t, dir, "switch", "--quiet", "main")
	writeFile(t, filepath.Join(dir, "g.txt"), window+padding+strings.Replace(window, "same", "changed", 1))
	rawGit(t, dir, "commit", "--quiet", "-am", "the same edit, elsewhere")
	elsewhere := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	if a, b := patchID(t, dir, first), patchID(t, dir, elsewhere); a != b {
		t.Fatalf("the two commits have different patch identifiers (%s, %s), so this is not the section 9 case", a, b)
	}
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.Incorporated(ctx(t), []string{first}, "main")
	if err != nil || got {
		t.Errorf("Incorporated(same hunk elsewhere, main) = %v, %v; want false with no error", got, err)
	}
}

// A revision that does not resolve locally is inconclusive rather than false,
// and an empty commit set is refused before git runs.
func TestIncorporatedRefusesWhatItCannotResolve(t *testing.T) {
	gitEnvironment(t)
	dir, _, first, _ := incorporationRepo(t)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := repo.Incorporated(ctx(t), []string{"nowhere"}, "main"); !errors.Is(err, vcs.ErrInconclusive) || !errors.Is(err, vcs.ErrRefNotFound) || got {
		t.Errorf("Incorporated(unknown commit) = %v, %v; want ErrInconclusive wrapping ErrRefNotFound", got, err)
	}
	if got, err := repo.Incorporated(ctx(t), []string{first}, "nowhere"); !errors.Is(err, vcs.ErrInconclusive) || !errors.Is(err, vcs.ErrRefNotFound) || got {
		t.Errorf("Incorporated(into unknown tip) = %v, %v; want ErrInconclusive wrapping ErrRefNotFound", got, err)
	}
	if got, err := repo.Incorporated(ctx(t), nil, "main"); !errors.Is(err, vcs.ErrInvalidArgument) || got {
		t.Errorf("Incorporated(no commits) = %v, %v; want ErrInvalidArgument", got, err)
	}
	if got, err := repo.Incorporated(ctx(t), []string{"--all"}, "main"); !errors.Is(err, vcs.ErrInvalidArgument) || got {
		t.Errorf("Incorporated(option-shaped commit) = %v, %v; want ErrInvalidArgument", got, err)
	}
}

// Incorporated needs a git above the package floor and checks for it itself.
// The stand-in git reports whatever version the test names: one below the
// floor is refused as unsupported and inconclusive, a version line the check
// cannot read is refused the same way rather than taken as adequate, and one
// at the floor passes the check, which the error that follows shows by not
// being ErrUnsupportedGit.
func TestIncorporatedRefusesAGitBelowItsFloor(t *testing.T) {
	gitEnvironment(t)
	_, exe := useFakeGit(t)
	repo, err := vcs.OpenBare(ctx(t), fakeBareDir(t), vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenBare against the stand-in git: %v", err)
	}

	for _, reported := range []string{"2.37.9", "1.99.0", "", "unknown"} {
		t.Setenv(fakeGitVersion, reported)
		got, err := repo.Incorporated(ctx(t), []string{fakeCommit}, fakeCommit)
		if !errors.Is(err, vcs.ErrUnsupportedGit) || !errors.Is(err, vcs.ErrInconclusive) || got {
			t.Errorf("Incorporated with git reporting %q = %v, %v; want ErrUnsupportedGit and ErrInconclusive", reported, got, err)
		}
	}
	for _, reported := range []string{"2.38.0", "2.43.0.windows.1", "3.0.0-rc1"} {
		t.Setenv(fakeGitVersion, reported)
		_, err := repo.Incorporated(ctx(t), []string{fakeCommit}, fakeCommit)
		if errors.Is(err, vcs.ErrUnsupportedGit) {
			t.Errorf("Incorporated with git reporting %q was refused at the floor: %v", reported, err)
		}
	}
}

// patchID returns the stable patch identifier of a commit's change, read with
// git directly, so a test can show two commits are the case PRD section 9
// names before asserting what Incorporated makes of them.
func patchID(t *testing.T, dir, commit string) string {
	t.Helper()
	diff := exec.Command("git", "diff-tree", "-p", commit)
	diff.Dir = dir
	patch, err := diff.Output()
	if err != nil {
		t.Fatalf("git diff-tree %s: %v", commit, err)
	}
	id := exec.Command("git", "patch-id", "--stable")
	id.Dir = dir
	id.Stdin = strings.NewReader(string(patch))
	out, err := id.Output()
	if err != nil {
		t.Fatalf("git patch-id: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatalf("git patch-id printed nothing for %s", commit)
	}
	return fields[0]
}
