package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitRunner invokes git directly. This package is the documented exception to
// the rule that internal/vcs is the only place git is invoked from; doc.go
// says why, and the isolation the exception is granted on is applied here.
type gitRunner struct {
	// binary is the git to run, "git" unless a caller named another.
	binary string
	// env is the whole environment every invocation runs with. It is built
	// rather than inherited, so nothing an ancestor process left behind
	// reaches git: a GIT_DIR or a GIT_TEMPLATE_DIR in the environment of
	// whatever ran the build would otherwise decide what a scenario holds.
	env []string
}

// fixtureIdentity is the author and committer every commit here is made under,
// and the timestamp they are made at. Stating both is what keeps two builds of
// one scenario differing only where their absolute paths differ.
const (
	fixtureName  = "Assistant Fixture"
	fixtureEmail = "fixture@assistant.invalid"
	fixtureDate  = "2026-01-01T00:00:00+00:00"
)

// newGitRunner writes the configuration file every invocation reads and
// returns a runner bound to it. home is a directory this package owns; the
// file is written inside it.
func newGitRunner(binary, home string) (*gitRunner, error) {
	if binary == "" {
		binary = "git"
	}
	config := filepath.Join(home, "gitconfig")
	content := "[user]\n\tname = " + fixtureName + "\n\temail = " + fixtureEmail + "\n" +
		"[init]\n\tdefaultBranch = " + DefaultBranch + "\n" +
		"[commit]\n\tgpgsign = false\n" +
		"[tag]\n\tgpgsign = false\n" +
		"[core]\n\tautocrlf = false\n"
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("fixture: writing the git configuration the build runs under: %w", err)
	}
	return &gitRunner{
		binary: binary,
		env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + home,
			"GIT_CONFIG_GLOBAL=" + config,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=" + fixtureName,
			"GIT_AUTHOR_EMAIL=" + fixtureEmail,
			"GIT_AUTHOR_DATE=" + fixtureDate,
			"GIT_COMMITTER_NAME=" + fixtureName,
			"GIT_COMMITTER_EMAIL=" + fixtureEmail,
			"GIT_COMMITTER_DATE=" + fixtureDate,
		},
	}, nil
}

// run invokes git in dir and returns its trimmed standard output. A failure
// carries git's own message, because a fixture that fails to build is debugged
// from what git said and nothing else here knows more.
func (g *gitRunner) run(dir string, args ...string) (string, error) {
	cmd := exec.Command(g.binary, args...)
	cmd.Dir = dir
	cmd.Env = g.env
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("fixture: git %s in %s: %w: %s",
			strings.Join(args, " "), dir, err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// asExitError is errors.As specialized to *exec.ExitError, kept here so run
// reads as one statement rather than four.
func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // exec.Cmd.Output returns this unwrapped
		*target = ee
		return true
	}
	return false
}

// commitAll stages everything in the working copy and commits it, returning
// the commit identifier. It refuses an empty commit, so a plant that meant to
// change something and changed nothing fails the build rather than producing a
// scenario that is quietly missing a condition.
func (g *gitRunner) commitAll(dir, message string) (string, error) {
	if _, err := g.run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.run(dir, "commit", "--quiet", "-m", message); err != nil {
		return "", err
	}
	return g.run(dir, "rev-parse", "HEAD")
}

// writeFile writes one file under root, creating its parents. mode is applied
// as given, because the executable bit is part of what a planted hook is: a
// harness distribution writes an executable, and a plant that wrote a
// non-executable file would be a different condition.
func writeFile(root, rel string, mode os.FileMode, content string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("fixture: creating the parent of %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("fixture: writing %s: %w", path, err)
	}
	// WriteFile does not lower an existing file's mode, and a plant that
	// rewrites a file expects the mode it asked for.
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("fixture: setting the mode of %s: %w", path, err)
	}
	return nil
}

// copyTree copies the directory at src to dst, preserving file modes. It is
// the route the copied-working-directory condition arrives by: that condition
// is about a working copy whose configuration was inherited by being copied,
// and writing the same configuration into a fresh directory would produce a
// state the product's own path never reaches.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
}
