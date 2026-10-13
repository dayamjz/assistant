package vcs_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// An observation of a main working copy names its own .git as both the
// repository directory and the common directory, its root as the toplevel,
// its remotes with every URL, and the branch HEAD is on. The values are
// compared against what git reports directly, by path identity, since the
// temporary directory may be reached through a link.
func TestObserveReportsAMainWorkingCopy(t *testing.T) {
	gitEnvironment(t)
	dir, _, _ := sourceRepo(t)
	rawGit(t, dir, "remote", "add", "up", "https://example.invalid/up.git")
	rawGit(t, dir, "remote", "set-url", "--add", "--push", "up", "https://example.invalid/push.git")
	rawGit(t, dir, "remote", "add", "assistant", filepath.Join(dir, "gate.git"))

	obs, err := vcs.Observe(ctx(t), dir)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !samePath(t, obs.Dir, dir) || !samePath(t, obs.Toplevel, dir) {
		t.Errorf("Dir %s and Toplevel %s, want both %s", obs.Dir, obs.Toplevel, dir)
	}
	if !samePath(t, obs.GitDir, filepath.Join(dir, ".git")) {
		t.Errorf("GitDir %s, want %s", obs.GitDir, filepath.Join(dir, ".git"))
	}
	if !samePath(t, obs.CommonDir, obs.GitDir) {
		t.Errorf("CommonDir %s differs from GitDir %s in a main working copy", obs.CommonDir, obs.GitDir)
	}
	want := []vcs.Remote{
		{Name: "assistant", URLs: []string{filepath.Join(dir, "gate.git")}},
		{Name: "up", URLs: []string{"https://example.invalid/up.git"}, PushURLs: []string{"https://example.invalid/push.git"}},
	}
	if !reflect.DeepEqual(obs.Remotes, want) {
		t.Errorf("Remotes %+v, want %+v", obs.Remotes, want)
	}
	if obs.Branch != "main" || obs.Detached {
		t.Errorf("Branch %q Detached %v, want main on a branch", obs.Branch, obs.Detached)
	}
}

// A linked worktree reports its own entry as GitDir and the repository it was
// added from as CommonDir, which is the difference that tells one from a
// clone. A detached HEAD is reported as such with no branch.
func TestObserveTellsALinkedWorktreeFromACopy(t *testing.T) {
	gitEnvironment(t)
	dir, first, _ := sourceRepo(t)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if _, err := repo.AddWorktree(ctx(t), vcs.WorktreeSpec{Path: linked, Commit: first}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	obs, err := vcs.Observe(ctx(t), linked)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !samePath(t, obs.Toplevel, linked) {
		t.Errorf("Toplevel %s, want %s", obs.Toplevel, linked)
	}
	if !samePath(t, obs.CommonDir, filepath.Join(dir, ".git")) {
		t.Errorf("CommonDir %s, want the repository's %s", obs.CommonDir, filepath.Join(dir, ".git"))
	}
	if samePath(t, obs.GitDir, obs.CommonDir) {
		t.Errorf("GitDir %s equals CommonDir in a linked worktree", obs.GitDir)
	}
	if !obs.Detached || obs.Branch != "" {
		t.Errorf("Branch %q Detached %v, want detached", obs.Branch, obs.Detached)
	}
	if len(obs.Remotes) != 0 {
		t.Errorf("Remotes %+v, want none", obs.Remotes)
	}
}

// An observation from a subdirectory keeps Dir as the directory asked about
// and reports the working copy's root as Toplevel, so a caller can tell the
// two apart. A directory that is not in a working copy is ErrNotARepository.
func TestObserveFromASubdirectoryAndFromNoRepository(t *testing.T) {
	gitEnvironment(t)
	dir, _, _ := sourceRepo(t)
	sub := filepath.Join(dir, "docs")

	obs, err := vcs.Observe(ctx(t), sub)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !samePath(t, obs.Dir, sub) || !samePath(t, obs.Toplevel, dir) {
		t.Errorf("Dir %s Toplevel %s, want %s and %s", obs.Dir, obs.Toplevel, sub, dir)
	}

	if _, err := vcs.Observe(ctx(t), t.TempDir()); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("Observe of a plain directory: %v, want ErrNotARepository", err)
	}
}
