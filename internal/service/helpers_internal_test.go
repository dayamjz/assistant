package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
)

// gatedSubject is a working copy with one commit, a gate, and that commit in
// the gate, recorded as this home's repository. It is what these tests need
// because it is what a run needs: the run's isolated copy is a linked worktree
// of the gate, and create refuses a run whose head the gate does not hold.
//
// It returns the working path and the commit the gate took, which is the head
// a run started here validates.
//
// Before the copy existed these tests recorded a repository whose working path
// was the home root, which is not a repository at all. Nothing needed it to be
// one, so nothing said so.
type gatedSubject struct {
	workingPath string
	head        string
}

// newGatedSubject builds one, in the shape assistant init leaves behind.
func newGatedSubject(t *testing.T, h *home.Home, records *store.Store) gatedSubject {
	t.Helper()
	dir, err := os.MkdirTemp("", "subject")
	if err != nil {
		t.Fatalf("making a subject repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")

	// The recorded path is the resolved one, which is what the service
	// compares a working copy against.
	workingPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}

	command := filepath.Join(t.TempDir(), "assistant")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing the hook command: %v", err)
	}
	spec := gate.Spec{Home: h.Root(), WorkingPath: workingPath, Command: command}
	if _, err := gate.Initialize(t.Context(), spec, gate.WithIndex(records)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	head, err := gate.TakeBranch(t.Context(), spec, "main", gate.WithIndex(records))
	if err != nil {
		t.Fatalf("putting the branch in the gate: %v", err)
	}

	if _, err := records.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   workingPath,
		UpstreamURL:   "https://example.invalid/o/r.git",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return gatedSubject{workingPath: workingPath, head: head}
}

// rawGit runs git directly rather than through internal/vcs, because a fixture
// built with the code under test could not show that code wrong. It mirrors
// the isolation the external test helper of the same shape applies.
func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
