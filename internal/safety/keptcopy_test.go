package safety_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// gitIn runs git directly in dir, for building the repositories
// VerifyKeptCopy is asked about: a fixture built through the code under
// test could not show it wrong. Automatic maintenance is off, so no git a
// test starts outlives its temporary directory (#119), and the user's own
// configuration is not read.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// keptCopies is a clone with a commit, standing in a directory of its own,
// and a linked worktree of it, each with the repository directory it is
// recorded with, and another repository beside them.
type keptCopies struct {
	parent, other string
	clone, linked safety.KeptCopy
}

func newKeptCopies(t *testing.T) keptCopies {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := keptCopies{parent: filepath.Join(base, "parent"), other: filepath.Join(base, "other")}
	clone := filepath.Join(k.parent, "copy")
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "init", "--quiet", ".")
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", "a.txt")
	gitIn(t, clone, "commit", "--quiet", "-m", "work")
	linked := filepath.Join(base, "linked")
	gitIn(t, clone, "worktree", "add", "--quiet", "--detach", linked)
	gitIn(t, base, "clone", "--quiet", clone, k.other)
	k.clone = safety.KeptCopy{Worktree: clone, GitDir: filepath.Join(clone, ".git"), CommonDir: filepath.Join(clone, ".git")}
	k.linked = safety.KeptCopy{Worktree: linked, GitDir: gitIn(t, linked, "rev-parse", "--absolute-git-dir"), CommonDir: filepath.Join(clone, ".git")}
	return k
}

// VerifyKeptCopy answers whether a kept copy is the one its registry
// recorded, through git and the file system, and refuses with a
// *CopyRefusal matching ErrCopyNotVerified, naming why, every way it is
// not (round 28). The positive controls: a clone and a linked worktree as
// recorded pass, and so does a clone holding a link to a file and a link
// to nothing, which are content. Each refusal: the copy's path a link to
// it; its .git a link to another repository's; a worktree recorded with
// another worktree's record; the clone's .git without its HEAD, so git
// reads the checkout around it, and again with that checkout's
// core.worktree naming the clone, so git reports the clone as the working
// tree and reads the other .git; a linked worktree whose record names
// another repository as the one it shares, so git would read and write
// its objects and references there (round 44); a copy recorded with no
// shared repository directory; a link in the repository directory; and a
// link in the working tree to a directory.
func TestAKeptCopyIsTheOneItsRegistryRecorded(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	t.Run("as recorded, the positive controls", func(t *testing.T) {
		k := newKeptCopies(t)
		if err := os.Symlink("a.txt", filepath.Join(k.clone.Worktree, "to-a-file")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("nowhere", filepath.Join(k.clone.Worktree, "to-nothing")); err != nil {
			t.Fatal(err)
		}
		for _, want := range []safety.KeptCopy{k.clone, k.linked} {
			if err := safety.VerifyKeptCopy(ctx, want); err != nil {
				t.Fatalf("VerifyKeptCopy(%+v) = %v; want it verified", want, err)
			}
		}
	})
	for _, c := range []struct {
		name, why string
		make      func(t *testing.T, k keptCopies) safety.KeptCopy
	}{
		{"the copy's path a link to it", "is not a directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			link := filepath.Join(filepath.Dir(k.other), "link")
			if err := os.Symlink(k.clone.Worktree, link); err != nil {
				t.Fatal(err)
			}
			return safety.KeptCopy{Worktree: link, GitDir: filepath.Join(link, ".git")}
		}},
		{"its .git a link to another repository's", "has no repository directory of its own", func(t *testing.T, k keptCopies) safety.KeptCopy {
			if err := os.RemoveAll(k.clone.GitDir); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(k.other, ".git"), k.clone.GitDir); err != nil {
				t.Fatal(err)
			}
			return k.clone
		}},
		{"a worktree recorded with another's record", "is read by git through the repository directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			return safety.KeptCopy{Worktree: k.linked.Worktree, GitDir: k.clone.GitDir, CommonDir: k.clone.CommonDir}
		}},
		{"its .git without HEAD inside another checkout", "is read by git as part of the working tree", func(t *testing.T, k keptCopies) safety.KeptCopy {
			gitIn(t, k.parent, "init", "--quiet", ".")
			if err := os.Remove(filepath.Join(k.clone.GitDir, "HEAD")); err != nil {
				t.Fatal(err)
			}
			return k.clone
		}},
		{"that checkout's core.worktree naming it", "is read by git through the repository directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			gitIn(t, k.parent, "init", "--quiet", ".")
			gitIn(t, k.parent, "config", "core.worktree", k.clone.Worktree)
			if err := os.Remove(filepath.Join(k.clone.GitDir, "HEAD")); err != nil {
				t.Fatal(err)
			}
			return k.clone
		}},
		{"a worktree whose record names another repository as the one it shares", "is read by git as sharing the repository directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			// The other repository is a clone of the worktree's, so git
			// still reads the worktree, HEAD and all, through the
			// redirected record: only the binding refuses it.
			commondir := filepath.Join(k.linked.GitDir, "commondir")
			if err := os.WriteFile(commondir, []byte(filepath.Join(k.other, ".git")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := gitIn(t, k.linked.Worktree, "rev-parse", "HEAD"); got == "" {
				t.Fatal("git no longer reads the worktree through the redirected record")
			}
			return k.linked
		}},
		{"recorded with no shared repository directory", "is recorded with no shared repository directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			return safety.KeptCopy{Worktree: k.clone.Worktree, GitDir: k.clone.GitDir}
		}},
		{"a link in its repository directory", "has a symbolic link in its repository directory", func(t *testing.T, k keptCopies) safety.KeptCopy {
			heads := filepath.Join(k.clone.GitDir, "refs", "heads")
			if err := os.RemoveAll(heads); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(k.other, ".git", "refs", "heads"), heads); err != nil {
				t.Fatal(err)
			}
			return k.clone
		}},
		{"a link in its working tree to a directory", "has a symbolic link a path inside it could pass through", func(t *testing.T, k keptCopies) safety.KeptCopy {
			if err := os.Symlink(k.other, filepath.Join(k.clone.Worktree, "d")); err != nil {
				t.Fatal(err)
			}
			return k.clone
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newKeptCopies(t)
			want := c.make(t, k)
			err := safety.VerifyKeptCopy(ctx, want)
			var refusal *safety.CopyRefusal
			if !errors.Is(err, safety.ErrCopyNotVerified) || !errors.As(err, &refusal) || !strings.HasPrefix(refusal.Reason, c.why) {
				t.Fatalf("VerifyKeptCopy with %s = %v; want a *CopyRefusal saying it %s", c.name, err, c.why)
			}
		})
	}
}
