package fixture

import (
	"errors"
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
	// binary is the resolved path of the git to run. It is resolved rather
	// than kept as the name it was asked for, so a scenario can record which
	// git built it and a plant applied later runs that one.
	binary string
	// home is the directory this package owns, and config is the
	// configuration file inside it every invocation reads.
	home   string
	config string
	// env is the whole environment every invocation runs with. It is the
	// caller's environment with the variables that would let an ancestor
	// process decide what a scenario holds taken out, and this package's own
	// settings written over the top. What reaches git is therefore everything
	// else the platform put there, which is what git and the processes it
	// starts need on Windows, and nothing that redirects it.
	env []string
	// executables are the planted executables written but not yet committed,
	// each with the working copy it belongs to. The commit that stages one
	// states its mode in the index rather than leaving git to infer it from
	// the file, because git only reads the filesystem's executable bit where
	// core.fileMode is true.
	executables []plantedExecutable
}

// plantedExecutable is one planted executable awaiting the commit that stages
// it, named relative to the working copy it was written into.
type plantedExecutable struct {
	dir string
	rel string
}

// fixtureIdentity is the author and committer every commit here is made under,
// and the timestamp they are made at. Stating both is what keeps two builds of
// one scenario differing only where their absolute paths differ.
const (
	fixtureName  = "Assistant Fixture"
	fixtureEmail = "fixture@assistant.invalid"
	fixtureDate  = "2026-01-01T00:00:00+00:00"
)

// gitConfigName is the configuration file newGitRunner writes, relative to the
// home it is given.
const gitConfigName = "gitconfig"

// newGitRunner writes the configuration file every invocation reads and
// returns a runner bound to it. home is a directory this package owns; the
// file is written inside it. binary is the git to run, empty for git on PATH,
// and it is resolved here so that a scenario records the git it was built with
// rather than a name that resolves differently elsewhere.
func newGitRunner(binary, home string) (*gitRunner, error) {
	if binary == "" {
		binary = "git"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("fixture: locating the git to build with (%s): %w", binary, err)
	}
	config := filepath.Join(home, gitConfigName)
	content := "[user]\n\tname = " + fixtureName + "\n\temail = " + fixtureEmail + "\n" +
		"[init]\n\tdefaultBranch = " + DefaultBranch + "\n" +
		"[commit]\n\tgpgsign = false\n" +
		"[tag]\n\tgpgsign = false\n" +
		"[core]\n\tautocrlf = false\n"
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("fixture: writing the git configuration the build runs under: %w", err)
	}
	return &gitRunner{
		binary: resolved,
		home:   home,
		config: config,
		env:    gitEnvironment(home, config),
	}, nil
}

// redirectingVars are the environment variables taken out of the inherited
// environment before git is invoked here. They are the ones that would let
// whatever ran the build decide what a scenario holds: which repository an
// invocation resolves to, where its configuration comes from, and what a
// repository this package creates is born with.
//
// The approach is internal/vcs's rather than a fresh environment, and for the
// reason that package gives: an environment built from nothing drops the
// variables the platform itself needs, and on Windows git and the processes it
// starts do not work without SystemRoot, ComSpec, TEMP and their neighbours.
// This list is written by hand, so it covers what is on it and nothing else.
//
// Where it departs from internal/vcs is the configuration file location.
// internal/vcs deliberately keeps GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM,
// because a configuration file reached through them is the channel this fixture
// plants against. Here they are this package's to state, so they are taken out
// and written back from the file newGitRunner wrote.
var redirectingVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_CONFIG_SYSTEM",
	"XDG_CONFIG_HOME",
	"GIT_TEMPLATE_DIR",
	"GIT_EXEC_PATH",
	"GIT_EXTERNAL_DIFF",
	"GIT_EXTERNAL_DIFF_TRUST_EXIT_CODE",
	"GIT_SSH",
	"GIT_SSH_COMMAND",
	"GIT_SSH_VARIANT",
	"GIT_PROXY_COMMAND",
	"GIT_ALLOW_PROTOCOL",
	"GIT_REDIRECT_STDIN",
	"GIT_REDIRECT_STDOUT",
	"GIT_REDIRECT_STDERR",
}

// redirectingPrefixes are variable name prefixes taken out for the same reason
// as redirectingVars. GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n are the numbered
// halves of the GIT_CONFIG_COUNT mechanism.
var redirectingPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// gitEnvironment is the whole environment an invocation runs with: the caller's
// own, with redirectingVars and redirectingPrefixes removed and the settings
// this package states written over the top.
//
// The stated settings are the isolation doc.go claims. GIT_CONFIG_GLOBAL and
// GIT_CONFIG_NOSYSTEM point git at the file this package wrote and at no other,
// so a developer's own git configuration cannot change what a scenario holds,
// and the identity and dates are fixed, so two builds of one scenario differ
// only where their absolute paths differ.
func gitEnvironment(home, config string) []string {
	stated := [][2]string{
		{"HOME", home},
		{"GIT_CONFIG_GLOBAL", config},
		{"GIT_CONFIG_NOSYSTEM", "1"},
		{"GIT_TERMINAL_PROMPT", "0"},
		{"GIT_AUTHOR_NAME", fixtureName},
		{"GIT_AUTHOR_EMAIL", fixtureEmail},
		{"GIT_AUTHOR_DATE", fixtureDate},
		{"GIT_COMMITTER_NAME", fixtureName},
		{"GIT_COMMITTER_EMAIL", fixtureEmail},
		{"GIT_COMMITTER_DATE", fixtureDate},
	}
	drop := make(map[string]struct{}, len(redirectingVars)+len(stated))
	for _, name := range redirectingVars {
		drop[name] = struct{}{}
	}
	for _, kv := range stated {
		drop[kv[0]] = struct{}{}
	}

	base := os.Environ()
	out := make([]string, 0, len(base)+len(stated))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, skip := drop[name]; skip {
			continue
		}
		if hasAnyPrefix(name, redirectingPrefixes) {
			continue
		}
		out = append(out, entry)
	}
	for _, kv := range stated {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// hasAnyPrefix reports whether s starts with any of the prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// GitInvocation returns the git binary the scenario was built with and the
// whole environment it has to be invoked in, so a caller in another process
// reads the scenario the way the build wrote it.
//
// It refuses a scenario carrying neither rather than falling back to git on
// PATH under whatever configuration the caller happens to have, because a
// scenario read under a developer's own git configuration is not the scenario
// this package built.
func (s Scenario) GitInvocation() (string, []string, error) {
	binary, home, config := s.Paths[GitBinaryKey], s.Paths[GitHomeKey], s.Paths[GitConfigKey]
	if binary == "" || home == "" || config == "" {
		return "", nil, fmt.Errorf("fixture: scenario %s does not carry all of %s, %s, and %s, so git "+
			"cannot be run the way the build ran it", s.Name, GitBinaryKey, GitHomeKey, GitConfigKey)
	}
	return binary, gitEnvironment(home, config), nil
}

// gitConfigPathValue renders a filesystem path as a git configuration value
// that git parses back to the same path.
//
// Two things about the format are load-bearing. A backslash introduces an
// escape in a configuration value, so a Windows path written raw is either
// refused as a bad config line or folded into a different path, and the
// separator is written forward-slashed because git accepts that form on every
// platform. Quoting is what carries a path holding a space or a comment
// character; the quote and the backslash are then the two bytes that have to
// be escaped inside it.
func gitConfigPathValue(path string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range filepath.ToSlash(path) {
		if r == '"' || r == '\\' {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte('"')
	return out.String()
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
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("fixture: git %s in %s: %w: %s",
			strings.Join(args, " "), dir, err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// commitAll stages everything in the working copy and commits it, returning
// the commit identifier. It refuses an empty commit, so a plant that meant to
// change something and changed nothing fails the build rather than producing a
// scenario that is quietly missing a condition.
//
// Every planted executable written into this working copy has its mode stated
// in the index before the commit is made. Leaving the mode to be inferred from
// the file would commit a plain file wherever git is configured not to read
// the filesystem's executable bit, and the executable bit is part of what a
// planted harness installation is rather than a detail of how it was written.
func (g *gitRunner) commitAll(dir, message string) (string, error) {
	if _, err := g.run(dir, "add", "-A"); err != nil {
		return "", err
	}
	var pending []plantedExecutable
	for _, e := range g.executables {
		if e.dir != dir {
			pending = append(pending, e)
			continue
		}
		if _, err := g.run(dir, "update-index", "--add", "--chmod=+x", "--", e.rel); err != nil {
			return "", err
		}
	}
	g.executables = pending
	if _, err := g.run(dir, "commit", "--quiet", "-m", message); err != nil {
		return "", err
	}
	return g.run(dir, "rev-parse", "HEAD")
}

// requireDrained reports the planted executables that were written and never
// staged by a commit. A pending entry is a file some other plant committed with
// the mode git infers rather than the mode this package stated, which is the
// executable bit lost wherever core.fileMode is false.
func (g *gitRunner) requireDrained() error {
	if len(g.executables) == 0 {
		return nil
	}
	first := g.executables[0]
	return fmt.Errorf("fixture: %d planted executable(s) were never staged by a commit, starting with "+
		"%s in %s, so their mode was left to be inferred rather than stated; every writeExecutable "+
		"needs a commitAll on the same working copy", len(g.executables), first.rel, first.dir)
}

// writeExecutable writes a planted executable into a working copy and records
// that the commit staging it must state mode 100755 in the index. Use it for
// anything planted that a commit has to carry as an executable; writeFile with
// an executable mode is for what only ever runs from the filesystem, such as a
// git template's hooks.
func (g *gitRunner) writeExecutable(dir, rel, content string) error {
	if err := writeFile(dir, rel, 0o755, content); err != nil {
		return err
	}
	g.executables = append(g.executables, plantedExecutable{dir: dir, rel: rel})
	return nil
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
