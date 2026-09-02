package vcs_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnvironment points git at a configuration file this test owns, so a
// developer's own git configuration cannot change what a test proves. It
// returns the path of that file so a test can append settings to it.
//
// It deliberately does not go through the package under test: a fixture built
// with the code being tested cannot show that code wrong.
func gitEnvironment(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = Test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return cfg
}

// appendConfig adds lines to the configuration file gitEnvironment created.
func appendConfig(t *testing.T, cfg, text string) {
	t.Helper()
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", cfg, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("append to %s: %v", cfg, err)
	}
}

// rawGit runs git directly, without the package under test, and fails the test
// if it does not succeed.
func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryRawGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// tryRawGit runs git directly and returns its combined output and error, for
// the cases where the failure is the point.
func tryRawGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sourceRepo builds a working copy with two commits that between them cover
// every change status the tests care about: an addition, a modification, a
// deletion, and a rename. It returns the directory and the two commit
// identifiers.
func sourceRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")

	writeFile(t, filepath.Join(dir, "kept.txt"), "kept, unchanged\n")
	writeFile(t, filepath.Join(dir, "edited.txt"), "first version\n")
	writeFile(t, filepath.Join(dir, "removed.txt"), "goes away\n")
	writeFile(t, filepath.Join(dir, "moved.txt"), longUniqueContent)
	writeFile(t, filepath.Join(dir, "docs", "nested.txt"), "nested\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	first = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(dir, "edited.txt"), "second version\n")
	rawGit(t, dir, "rm", "--quiet", "removed.txt")
	rawGit(t, dir, "mv", "moved.txt", "arrived.txt")
	writeFile(t, filepath.Join(dir, "added.txt"), "brand new\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "second")
	second = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	return dir, first, second
}

// longUniqueContent is long enough that git's rename detection scores a moved
// copy of it as a rename rather than as an unrelated add and delete.
const longUniqueContent = `line one of a file that is long enough to be recognizable
line two of a file that is long enough to be recognizable
line three of a file that is long enough to be recognizable
line four of a file that is long enough to be recognizable
line five of a file that is long enough to be recognizable
`

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c
}
