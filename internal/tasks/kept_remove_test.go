package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// plantTree writes a small copy-shaped tree under a fresh root: files at
// the top and in a nested directory, a link, and a .git directory.
func plantTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "copy")
	for path, content := range map[string]string{
		"a.txt":            "a\n",
		"nested/b.txt":     "b\n",
		"nested/deep/c.md": "c\n",
		".git/HEAD":        "ref: refs/heads/main\n",
		".git/objects/x":   "object\n",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// removeAsRead removes a tree only as its state recorded it. Unchanged, the
// whole tree goes. A file whose bytes or mode changed after the state was
// read refuses with ErrLossChanged and stays under its name holding what
// was written; a file or directory added after the read keeps its directory
// from being removed and refuses too, standing as it was made; and an entry
// already gone refuses nothing. The repository directory is removed after
// everything beside it, so a refusal among the working files leaves the
// repository readable. Each refusal's positive control is the unchanged
// tree, which is removed.
func TestARemovalRemovesOnlyWhatItsStateRecorded(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Run("unchanged", func(t *testing.T) {
		root := plantTree(t)
		state, err := readCopyState(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := removeAsRead(root, state); err != nil {
			t.Fatalf("removing the unchanged tree: %v", err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the tree still stands: %v", err)
		}
	})
	for name, change := range map[string]func(t *testing.T, root string) string{
		"a file's bytes": func(t *testing.T, root string) string {
			p := filepath.Join(root, "nested", "b.txt")
			if err := os.WriteFile(p, []byte("written after the read\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a file's mode": func(t *testing.T, root string) string {
			p := filepath.Join(root, "nested", "b.txt")
			if err := os.Chmod(p, 0o400); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a link's target": func(t *testing.T, root string) string {
			p := filepath.Join(root, "link")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("nested/b.txt", p); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a file added": func(t *testing.T, root string) string {
			p := filepath.Join(root, "nested", "deep", "new.txt")
			if err := os.WriteFile(p, []byte("added after the read\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a directory added": func(t *testing.T, root string) string {
			p := filepath.Join(root, "nested", "made")
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := plantTree(t)
			state, err := readCopyState(root)
			if err != nil {
				t.Fatal(err)
			}
			changed := change(t, root)
			want, err := stateOf(changed)
			if err != nil {
				t.Fatal(err)
			}
			err = removeAsRead(root, state)
			if !errors.Is(err, ErrLossChanged) {
				t.Fatalf("removing the tree after %s changed = %v; want ErrLossChanged", name, err)
			}
			if added := strings.HasSuffix(name, "added"); added != strings.Contains(err.Error(), "was not there when the copy was read") {
				t.Fatalf("the refusal after %s reads %v; want it to name an entry the read never saw exactly when one was added", name, err)
			}
			got, err := stateOf(changed)
			if err != nil || got != want {
				t.Fatalf("%s reads %+v, %v after the refusal; want it as it was changed, %+v", changed, got, err, want)
			}
			if head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD")); err != nil || string(head) != "ref: refs/heads/main\n" {
				t.Fatalf("the repository directory's HEAD reads %q, %v; want it left, since it is removed last", head, err)
			}
			entries, err := os.ReadDir(filepath.Dir(changed))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".assistant-discard-") {
					t.Fatalf("%s was left moved aside beside %s", e.Name(), changed)
				}
			}
		})
	}
	t.Run("an entry made after its directory was listed", func(t *testing.T) {
		root := plantTree(t)
		state, err := readCopyState(root)
		if err != nil {
			t.Fatal(err)
		}
		deep := filepath.Join(root, "nested", "deep")
		made := filepath.Join(deep, "made-after-the-listing.txt")
		r := remover{afterListing: func(dir string) {
			if dir == deep {
				if err := os.WriteFile(made, []byte("made after the listing\n"), 0o600); err != nil {
					t.Error(err)
				}
			}
		}}
		if err := r.removeEntry(root, state); !errors.Is(err, ErrLossChanged) {
			t.Fatalf("removing the tree with an entry made after its directory was listed = %v; want ErrLossChanged", err)
		}
		if raw, err := os.ReadFile(made); err != nil || string(raw) != "made after the listing\n" {
			t.Fatalf("the entry made after the listing reads %q, %v; want it standing", raw, err)
		}
	})
	t.Run("an entry already gone", func(t *testing.T) {
		root := plantTree(t)
		state, err := readCopyState(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if err := removeAsRead(root, state); err != nil {
			t.Fatalf("removing the tree with an entry already gone: %v", err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the tree still stands: %v", err)
		}
	})
}

// renameNoReplace moves a file to a free name and refuses, moving nothing,
// to land on a name that stands, which is what lets a removal put back an
// entry it found changed without overwriting one made under its name
// since. The positive control is the move to a free name.
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

// recordOfGate takes a directory of the gate's own worktrees directory as a
// worktree's record, the positive control, also when it is reached through
// a link, and refuses one anywhere else: the gate's worktrees directory
// itself, a directory under a record, and a directory outside the gate.
func TestAWorktreeRecordIsTakenOnlyFromTheGatesWorktrees(t *testing.T) {
	principles.Cite(t, principles.P6)
	root := t.TempDir()
	gate := filepath.Join(root, "gate.git")
	record := filepath.Join(gate, "worktrees", "run")
	outside := filepath.Join(root, "elsewhere", ".git")
	for _, dir := range []string{filepath.Join(record, "logs"), outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(gate, linked); err != nil {
		t.Fatal(err)
	}
	if err := recordOfGate(record, gate); err != nil {
		t.Fatalf("the gate's own worktree record was refused: %v", err)
	}
	if err := recordOfGate(filepath.Join(linked, "worktrees", "run"), gate); err != nil {
		t.Fatalf("the record reached through a link was refused: %v", err)
	}
	for name, dir := range map[string]string{
		"the worktrees directory itself": filepath.Join(gate, "worktrees"),
		"a directory under a record":     filepath.Join(record, "logs"),
		"a directory outside the gate":   outside,
	} {
		if err := recordOfGate(dir, gate); !errors.Is(err, ErrRecordOutsideGate) {
			t.Errorf("%s, %s, reads %v as a worktree record; want ErrRecordOutsideGate", name, dir, err)
		}
	}
}
