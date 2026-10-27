package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// configSubject is a working copy on main with two commits, an annotated tag
// v1 on the first, and a linked worktree at the first, opened through the
// package under test, with the directory of each and the two commits.
func configSubject(t *testing.T) (repo *vcs.Repository, dir, linked, first, second string) {
	t.Helper()
	gitEnvironment(t)
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "a.txt"), "first\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	first = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "tag", "--annotate", "--message", "one", "v1")
	writeFile(t, filepath.Join(dir, "a.txt"), "second\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "second\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "second")
	second = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	linked = filepath.Join(t.TempDir(), "linked")
	rawGit(t, dir, "worktree", "add", "--quiet", "--detach", linked, first)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}
	return repo, dir, linked, first, second
}

// ConfigNames lists the keys git reads whose name matches a pattern, as
// git puts them on the wire: two remotes marked promisor are each named
// once, a key set twice is named once, a key that does not match is not
// named, and a pattern nothing matches is no names and no error.
func TestConfigNamesAreTheKeysGitReadsForAPattern(t *testing.T) {
	repo, dir, _, _, _ := configSubject(t)
	c := ctx(t)
	rawGit(t, dir, "config", "remote.one.promisor", "true")
	rawGit(t, dir, "config", "remote.Two.promisor", "false")
	rawGit(t, dir, "config", "--add", "remote.Two.promisor", "true")
	rawGit(t, dir, "config", "remote.one.partialclonefilter", "blob:none")
	got, err := repo.ConfigNames(c, `^remote\..*\.promisor$`)
	if err != nil || !slices.Equal(got, []string{"remote.one.promisor", "remote.Two.promisor"}) {
		t.Fatalf("ConfigNames = %q, %v; want remote.one.promisor and remote.Two.promisor", got, err)
	}
	if got, err := repo.ConfigNames(c, `^nothing\.matches$`); err != nil || len(got) != 0 {
		t.Fatalf("ConfigNames of a pattern nothing matches = %q, %v; want none", got, err)
	}
}

// GitPath names where git keeps what a working copy keeps of its own: in a
// linked worktree, its index and HEAD's reflog in its own directory in the
// main repository's worktrees directory, and its references in the common
// directory; in the main working copy, all under .git.
func TestGitPathNamesWhatAWorktreeKeepsOfItsOwn(t *testing.T) {
	_, dir, linked, _, _ := configSubject(t)
	c := ctx(t)
	wt, err := vcs.OpenWorktree(c, linked)
	if err != nil {
		t.Fatal(err)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"index":      filepath.Join(common, "worktrees", "linked", "index"),
		"logs/HEAD":  filepath.Join(common, "worktrees", "linked", "logs", "HEAD"),
		"refs/heads": filepath.Join(common, "refs", "heads"),
	} {
		got, err := wt.GitPath(c, name)
		if err != nil {
			t.Fatalf("GitPath(%q): %v", name, err)
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(got)); err == nil {
			got = filepath.Join(resolved, filepath.Base(got))
		}
		if got != want {
			t.Fatalf("GitPath(%q) in the linked worktree = %s; want %s", name, got, want)
		}
	}
}

// Configuration is read as git reads it: ConfigBool reports a key unset as unset, reads git's
// spellings of a boolean ("yes", "off") as git does, and refuses a value
// git does not read as one; ConfigValue reports the last of several values
// set, as git uses.
func TestConfigIsReadAsGitReadsIt(t *testing.T) {
	repo, dir, _, _, _ := configSubject(t)
	c := ctx(t)
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || set || value {
		t.Fatalf("ConfigBool of an unset key = %v, %v, %v; want unset", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "yes")
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || !set || !value {
		t.Fatalf("ConfigBool of yes = %v, %v, %v; want set and true", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "off")
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || !set || value {
		t.Fatalf("ConfigBool of off = %v, %v, %v; want set and false", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "sometimes")
	var cmdErr *vcs.CommandError
	if _, _, err := repo.ConfigBool(c, "core.sparseCheckout"); !errors.As(err, &cmdErr) {
		t.Fatalf("ConfigBool of a value that is no boolean = %v; want a *CommandError", err)
	}
	rawGit(t, dir, "config", "--unset", "core.sparseCheckout")
	rawGit(t, dir, "config", "--add", "x.y", "first")
	rawGit(t, dir, "config", "--add", "x.y", "last")
	if value, set, err := repo.ConfigValue(c, "x.y"); err != nil || !set || value != "last" {
		t.Fatalf("ConfigValue of a key set twice = %q, %v, %v; want the last, set", value, set, err)
	}
	if _, set, err := repo.ConfigValue(c, "x.unset"); err != nil || set {
		t.Fatalf("ConfigValue of an unset key = %v, %v; want unset", set, err)
	}
}

// ConfigValue answers as git puts the value on the wire, round 26's
// second finding: git config -z --get ends the last value set with a NUL,
// and exits 1 for a key not set, so an explicitly empty value is a value.
// Each case's want is derived from git's own bytes for the key, read
// directly: a key set to first and then to nothing, a key set only to
// nothing, a key with no value at all, and a key not set. Before,
// ConfigValue split git's lines and dropped an empty one, answering first
// for the first case and unset for the second.
func TestConfigValueIsWhatGitPutsOnTheWire(t *testing.T) {
	repo, dir, _, _, _ := configSubject(t)
	c := ctx(t)
	config := filepath.Join(dir, ".git", "config")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct{ name, section, key string }{
		{"first and then nothing", "[a]\n\ty = first\n\ty = \"\"\n", "a.y"},
		{"nothing alone", "[b]\n\ty =\n", "b.y"},
		{"no value at all", "[c]\n\ty\n", "c.y"},
		{"not set", "", "d.y"},
	} {
		t.Run(k.name, func(t *testing.T) {
			if err := os.WriteFile(config, append(append([]byte{}, raw...), k.section...), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := tryRawGit(dir, "config", "-z", "--get", k.key)
			wantSet := err == nil
			want := strings.TrimSuffix(out, "\x00")
			if !wantSet {
				want = ""
			}
			got, set, err := repo.ConfigValue(c, k.key)
			if err != nil || got != want || set != wantSet {
				t.Fatalf("ConfigValue(%s) = %q, %v, %v; want %q, %v as git puts it on the wire (%q)", k.key, got, set, err, want, wantSet, out)
			}
		})
	}
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, set, err := repo.ConfigValue(c, "a.y"); err != nil || set || got != "" {
		t.Fatalf("ConfigValue once the key is gone = %q, %v, %v; want unset", got, set, err)
	}
}
