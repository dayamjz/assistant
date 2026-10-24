package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// renameNoReplace moves a file to a free name and refuses, moving nothing,
// to land on a name that stands, which is what lets a discard put back an
// entry it set aside without overwriting one made under its name since.
// The positive control is the move to a free name.
func TestARenameThatMustNotReplaceRefusesToLandOnAName(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this build has a rename that refuses to replace only on Linux and macOS")
	}
	dir := t.TempDir()
	from, taken, free := filepath.Join(dir, "from"), filepath.Join(dir, "taken"), filepath.Join(dir, "free")
	for p, content := range map[string]string{from: "moved\n", taken: "made under the name\n"} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameNoReplace(from, taken); err == nil {
		t.Fatal("the rename replaced the file standing under the name")
	}
	if raw, err := os.ReadFile(taken); err != nil || string(raw) != "made under the name\n" {
		t.Fatalf("the name's file reads %q, %v; want it as it was made", raw, err)
	}
	if err := renameNoReplace(from, free); err != nil {
		t.Fatalf("renaming to a free name: %v", err)
	}
	if raw, err := os.ReadFile(free); err != nil || string(raw) != "moved\n" {
		t.Fatalf("the moved file reads %q, %v", raw, err)
	}
}

// copyRecord takes a run's copy's record from the gate's own registry and
// holds the copy's files to it without following them, the final reviews
// of e036141 and e5d54d3 and the one before: with two worktrees, A and B,
// of one gate, A's record is A's own, the positive control, also with A
// reached through a linked parent directory; and every way A's files can
// disagree with the gate is refused with ErrRecordNotTheCopys: A's .git a
// link to B's .git (the reviewer's sequence), A's .git naming B's record,
// A's .git naming a record outside the gate, A's path itself a link to B,
// A's .git a directory, a path the gate records no worktree at, and a gate
// whose registry cannot be read; and A's .git a link, or A's path a link,
// even to what names A's own record, since git writes neither.
func TestACopysRecordIsTheOneTheGateRecordsForIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	type pair struct {
		gate       *vcs.Repository
		a, b       string
		aRec, bRec string
	}
	setup := func(t *testing.T) pair {
		t.Helper()
		s := isolationSubject(t)
		bare, err := vcs.OpenBare(context.Background(), s.gate)
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "copies")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		p := pair{gate: bare, a: filepath.Join(real, "a"), b: filepath.Join(real, "b")}
		for _, dir := range []string{p.a, p.b} {
			rawGit(t, s.gate, "worktree", "add", "-q", "--detach", dir, "main")
		}
		p.aRec = strings.TrimSpace(rawGit(t, p.a, "rev-parse", "--absolute-git-dir"))
		p.bRec = strings.TrimSpace(rawGit(t, p.b, "rev-parse", "--absolute-git-dir"))
		return p
	}
	ctx := context.Background()
	t.Run("the copy's own, the positive control", func(t *testing.T) {
		p := setup(t)
		if got, err := copyRecord(ctx, p.gate, p.a); err != nil || got != p.aRec {
			t.Fatalf("copyRecord(A) = %s, %v; want A's record %s", got, err, p.aRec)
		}
		linked := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(filepath.Dir(p.a), linked); err != nil {
			t.Fatal(err)
		}
		if got, err := copyRecord(ctx, p.gate, filepath.Join(linked, "a")); err != nil || got != p.aRec {
			t.Fatalf("copyRecord(A through a linked parent) = %s, %v; want A's record %s", got, err, p.aRec)
		}
	})
	for name, disagree := range map[string]func(t *testing.T, p pair) string{
		"a .git linked to the other's .git": func(t *testing.T, p pair) string {
			if err := os.Remove(filepath.Join(p.a, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(p.b, ".git"), filepath.Join(p.a, ".git")); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a .git naming the other's record": func(t *testing.T, p pair) string {
			if err := os.WriteFile(filepath.Join(p.a, ".git"), []byte("gitdir: "+p.bRec+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a .git naming a record outside the gate": func(t *testing.T, p pair) string {
			outside := filepath.Join(t.TempDir(), "record")
			if err := os.MkdirAll(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.a, ".git"), []byte("gitdir: "+outside+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a copy path linked to the other copy": func(t *testing.T, p pair) string {
			linked := filepath.Join(filepath.Dir(p.a), "c")
			if err := os.Symlink(p.b, linked); err != nil {
				t.Fatal(err)
			}
			return linked
		},
		"a .git linked to a file naming the copy's own record": func(t *testing.T, p pair) string {
			planted := filepath.Join(t.TempDir(), "dotgit")
			if err := os.WriteFile(planted, []byte("gitdir: "+p.aRec+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(p.a, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(planted, filepath.Join(p.a, ".git")); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a copy path that is a link to where the copy now stands": func(t *testing.T, p pair) string {
			moved := filepath.Join(filepath.Dir(p.a), "a-moved")
			if err := os.Rename(p.a, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, p.a); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a .git that is a directory": func(t *testing.T, p pair) string {
			if err := os.Remove(filepath.Join(p.a, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(p.a, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
		"a path the gate records no worktree at": func(t *testing.T, p pair) string {
			other := filepath.Join(filepath.Dir(p.a), "unrecorded")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, ".git"), []byte("gitdir: "+p.aRec+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return other
		},
		"a gate whose registry cannot be read": func(t *testing.T, p pair) string {
			if err := os.Remove(filepath.Join(p.bRec, "gitdir")); err != nil {
				t.Fatal(err)
			}
			return p.a
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := setup(t)
			copyPath := disagree(t, p)
			if got, err := copyRecord(ctx, p.gate, copyPath); !errors.Is(err, ErrRecordNotTheCopys) {
				t.Fatalf("copyRecord with %s = %s, %v; want ErrRecordNotTheCopys", name, got, err)
			}
		})
	}
}
