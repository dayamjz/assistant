package vcs_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Installed answers with the version line of the real git, and a binary that
// is not there is reported by GitNotFound. The control is a binary that
// starts and fails: it is not GitNotFound, so the predicate is not true of
// every failure.
func TestInstalledReportsAGitThatStartsAndOneThatIsNotThere(t *testing.T) {
	gitEnvironment(t)
	c := ctx(t)
	line, err := vcs.Installed(c)
	if err != nil {
		t.Fatalf("Installed with the git on PATH: %v", err)
	}
	if !strings.HasPrefix(line, "git version ") {
		t.Errorf("Installed answered %q, want a git version line", line)
	}

	missing := filepath.Join(t.TempDir(), "no-git-here")
	_, err = vcs.Installed(c, vcs.WithGitBinary(missing))
	if err == nil {
		t.Fatal("Installed with a binary that does not exist answered a version")
	}
	if !vcs.GitNotFound(err) {
		t.Errorf("GitNotFound(%v) = false for a binary that does not exist", err)
	}
	if vcs.GitNotFound(errors.New("some other failure")) {
		t.Error("GitNotFound is true of an error no invocation produced")
	}
}

// A binary that starts and answers something other than a version line is
// refused rather than reported as installed, and that refusal is not
// GitNotFound: it started.
func TestInstalledRefusesABinaryThatIsNotGit(t *testing.T) {
	_, binary := useFakeGit(t)
	// The stand-in prints no version line unless one is set, which is a
	// binary that starts and is not git.
	t.Setenv(fakeGitVersion, "")
	_, err := vcs.Installed(ctx(t), vcs.WithGitBinary(binary))
	if err == nil {
		t.Fatal("Installed accepted a binary that printed no version line")
	}
	if vcs.GitNotFound(err) {
		t.Errorf("GitNotFound(%v) = true for a binary that started", err)
	}

	t.Setenv(fakeGitVersion, "2.45.1")
	line, err := vcs.Installed(ctx(t), vcs.WithGitBinary(binary))
	if err != nil || line != "git version 2.45.1" {
		t.Errorf("Installed = %q, %v; want the stand-in's version line", line, err)
	}
}
