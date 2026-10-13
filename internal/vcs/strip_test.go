package vcs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// StripRedirecting is the one list of what points git elsewhere, exported for
// a caller that starts a process this package does not. What it buys is shown
// against real git rather than asserted over the list: with GIT_DIR naming
// another repository, a git started directly in a working copy answers for
// that other repository, which is the hazard P11 names, and the same command
// under the stripped environment answers for the copy it was started in.
//
// The git here is run by the test and not by the package under test, on the
// terms helpers_test.go states: a filter checked through the package that
// applies it would be the package agreeing with itself.
func TestStripRedirectingKeepsGitInTheRepositoryItWasStartedIn(t *testing.T) {
	gitEnvironment(t)
	copy := t.TempDir()
	rawGit(t, copy, "init", "-q")
	other := t.TempDir()
	rawGit(t, other, "init", "-q")
	otherDir := filepath.Join(other, ".git")

	t.Setenv("GIT_DIR", otherDir)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(t.TempDir(), "evil"))
	t.Setenv("GIT_WORK_TREE", other)

	gitDir := func(env []string) string {
		t.Helper()
		cmd := exec.Command("git", "rev-parse", "--absolute-git-dir")
		cmd.Dir = copy
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git rev-parse in %s: %v\n%s", copy, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// The hazard exists: started directly, git follows the inherited GIT_DIR
	// to the other repository.
	if got := gitDir(os.Environ()); !samePath(t, got, otherDir) {
		t.Fatalf("git started directly under GIT_DIR answered %q, want the other repository %q; "+
			"the hazard this test is about is absent, so the stripped half below proves nothing",
			got, otherDir)
	}

	stripped := vcs.StripRedirecting(os.Environ())
	if got := gitDir(stripped); !samePath(t, got, filepath.Join(copy, ".git")) {
		t.Errorf("git under the stripped environment answered %q, want the copy's own %q",
			got, filepath.Join(copy, ".git"))
	}
	for _, entry := range stripped {
		name, _, _ := strings.Cut(entry, "=")
		switch {
		case name == "GIT_DIR", name == "GIT_WORK_TREE", name == "GIT_CONFIG_COUNT":
			t.Errorf("%s survived StripRedirecting", name)
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_"), strings.HasPrefix(name, "GIT_CONFIG_VALUE_"):
			t.Errorf("%s survived StripRedirecting", name)
		}
	}
	// What is kept is kept: the configuration-location variables are this
	// package's deliberate keeps, and the test's own isolation rests on them.
	if !slices.ContainsFunc(stripped, func(e string) bool { return strings.HasPrefix(e, "GIT_CONFIG_GLOBAL=") }) {
		t.Error("GIT_CONFIG_GLOBAL was stripped, and the package comment says it is kept")
	}
}

// StripRedirecting hands back a new slice and leaves its argument alone, and
// an entry naming no variable is dropped rather than carried.
func TestStripRedirectingDoesNotModifyItsArgument(t *testing.T) {
	in := []string{"GIT_DIR=/x", "KEEP=1", "no-equals", "GIT_CONFIG_VALUE_3=y", "PATH=/bin"}
	before := slices.Clone(in)
	got := vcs.StripRedirecting(in)
	if !slices.Equal(in, before) {
		t.Errorf("the argument was modified: %v", in)
	}
	if want := []string{"KEEP=1", "PATH=/bin"}; !slices.Equal(got, want) {
		t.Errorf("StripRedirecting(%v) = %v, want %v", in, got, want)
	}
}

// samePath compares two paths after resolving symbolic links, since a
// temporary directory may be reported through one.
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatalf("resolving %s: %v", a, err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("resolving %s: %v", b, err)
	}
	return ra == rb
}
