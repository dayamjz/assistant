//go:build unix

package vcs_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/vcs"
)

// armableGit writes a git that runs the real one until the returned arm
// function is called, and from then on records its process identifier and
// never exits. It returns the executable, arm, and what it recorded; a
// recorded process still running when the test ends is killed then.
func armableGit(t *testing.T) (binary string, arm func(), recorded func() []int) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "armed")
	pids := filepath.Join(dir, "pids")
	binary = filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"if [ -e '" + marker + "' ]; then\n" +
		"  echo $$ >> '" + pids + "'\n" +
		"  exec sleep 600\n" +
		"fi\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := procgroup.WriteProgram(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the armable git: %v", err)
	}
	arm = func() {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatalf("arming the git: %v", err)
		}
	}
	recorded = func() []int {
		data, err := os.ReadFile(pids)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("reading what the armable git recorded: %v", err)
		}
		var out []int
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				t.Fatalf("the armable git recorded %q, not a process identifier", field)
			}
			out = append(out, pid)
		}
		return out
	}
	t.Cleanup(func() {
		for _, pid := range recorded() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return binary, arm, recorded
}

// A worktree AddWorktree creates from a handle is opened with that handle's
// whole configuration, its invocation deadline included: a git that never
// exits, called on the new worktree's handle under a context with no deadline
// of its own, is ended at the deadline the parent was built with and not at
// DefaultInvocationDeadline. internal/service builds every handle it opens
// with the operator's git_deadline, and a run's copy is one AddWorktree made,
// so a child that fell back to the default would run git under a bound the
// operator did not set (P15).
func TestAWorktreeAddedFromAHandleKeepsItsInvocationDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	gitEnvironment(t)
	const deadline = 3 * time.Second
	binary, arm, recorded := armableGit(t)
	dir, first, _ := sourceRepo(t)
	parent, err := vcs.OpenWorktree(ctx(t), dir, vcs.WithGitBinary(binary), vcs.WithInvocationDeadline(deadline))
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	child, err := parent.AddWorktree(ctx(t), vcs.WorktreeSpec{Path: filepath.Join(t.TempDir(), "linked"), Commit: first})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	arm()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := child.ResolveCommit(context.WithoutCancel(ctx(t)), "HEAD")
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(hangLimit):
		t.Fatalf("a git that never exits, called on the added worktree, was still running after %s; "+
			"the parent's %s deadline did not reach the child", hangLimit, deadline)
	}
	if elapsed := time.Since(start); elapsed < deadline {
		t.Errorf("the call returned after %s, before the %s deadline", elapsed, deadline)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ResolveCommit = %v; want the call ended at its deadline", err)
	}
	pids := recorded()
	if len(pids) != 1 {
		t.Fatalf("the armable git recorded %d waiting invocations, want the one call", len(pids))
	}
	requireGone(t, pids)
}

// RootRefs' reading for a git without --include-root-refs takes only git's
// own answer that a name is no reference as one, and reports every other
// failure, the delta review of 5d7e396: a git that reports 2.36, so the
// reading by name runs, and that never exits when it is asked to resolve
// FOO_HEAD, is ended at the handle's invocation deadline, and RootRefs
// returns that failure rather than a list without FOO_HEAD. Before, every
// failure resolving a name read as "no reference", so a read the deadline
// cut short came back as a complete answer.
func TestRootRefsByNameReportsALookupItCouldNotFinish(t *testing.T) {
	principles.Cite(t, principles.P15)
	gitEnvironment(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	rawGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", "a")
	rawGit(t, dir, "update-ref", "FOO_HEAD", "HEAD")
	stub := t.TempDir()
	pids := filepath.Join(stub, "pids")
	binary := filepath.Join(stub, "git")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = --version ]; then echo 'git version 2.36.0'; exit 0; fi\n" +
		"  if [ \"$a\" = FOO_HEAD ]; then echo $$ >> '" + pids + "'; exec sleep 600; fi\n" +
		"done\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := procgroup.WriteProgram(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stand-in git: %v", err)
	}
	t.Cleanup(func() {
		data, _ := os.ReadFile(pids)
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	repo, err := vcs.OpenWorktree(ctx(t), dir, vcs.WithGitBinary(binary), vcs.WithInvocationDeadline(2*time.Second))
	if err != nil {
		t.Fatalf("OpenWorktree: %v", err)
	}
	roots, err := repo.RootRefs(context.WithoutCancel(ctx(t)))
	if err == nil {
		t.Fatalf("RootRefs with the lookup of FOO_HEAD cut short = %+v, nil; want the failure, not a list without it", roots)
	}
	if _, statErr := os.Stat(pids); statErr != nil {
		t.Fatalf("the stand-in was never asked to resolve FOO_HEAD (%v), so the test reached nothing: RootRefs = %+v, %v", statErr, roots, err)
	}
}
