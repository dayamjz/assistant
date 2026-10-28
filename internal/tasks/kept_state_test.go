package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// moveAsRead sets an entry aside only as it was read (PRD section 6,
// "Removing a discarded copy", step 2): a file, a directory and a symbolic
// link read beforehand move whole and unchanged, a file rewritten with the
// bytes it held is no change, and nothing at a path nothing was read at
// moves nothing. Changed bytes, a changed mode, an entry added to or
// removed from a directory, and an entry read but gone each refuse with
// ErrLossChanged. A name already standing where the entry goes refuses
// without moving anything or touching what stands there.
func TestAnEntryIsSetAsideOnlyAsItWasRead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this build has a rename that refuses to replace only on Linux and macOS")
	}
	cases := []struct {
		name    string
		make    func(t *testing.T, at string)
		between func(t *testing.T, at string)
		moved   bool
		changed bool
	}{
		{name: "a file as read", make: file("work\n"), moved: true},
		{name: "a directory as read", make: tree, moved: true},
		{name: "a symbolic link as read", make: link, moved: true},
		{name: "a file rewritten with its bytes", make: file("work\n"), between: rewrite("work\n"), moved: true},
		{name: "nothing read and nothing there", make: nil, moved: false},
		{name: "a file whose bytes changed", make: file("work\n"), between: rewrite("other\n"), moved: true, changed: true},
		{name: "a file whose mode changed", make: file("work\n"), between: func(t *testing.T, at string) {
			// From writeFile's 0o600 to a mode with no execute bit, so the
			// test writes no program (procgroup/guard's LoadWrites).
			if err := os.Chmod(at, 0o644); err != nil {
				t.Fatal(err)
			}
		}, moved: true, changed: true},
		{name: "a file added to a directory", make: tree, between: func(t *testing.T, at string) {
			writeFile(t, filepath.Join(at, "sub", "new.txt"), "added\n")
		}, moved: true, changed: true},
		{name: "a file gone from a directory", make: tree, between: func(t *testing.T, at string) {
			if err := os.Remove(filepath.Join(at, "sub", "b.txt")); err != nil {
				t.Fatal(err)
			}
		}, moved: true, changed: true},
		{name: "a file read and gone", make: file("work\n"), between: func(t *testing.T, at string) {
			if err := os.Remove(at); err != nil {
				t.Fatal(err)
			}
		}, moved: false, changed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			copyPath, aside := t.TempDir(), t.TempDir()
			at := filepath.Join(copyPath, "entry")
			if c.make != nil {
				c.make(t, at)
			}
			before, err := readCopyState(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if c.between != nil {
				c.between(t, at)
			}
			want, err := readCopyState(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			to := filepath.Join(aside, "tree", "entry")
			moved, err := moveAsRead(at, to, before, "entry")
			if moved != c.moved || errors.Is(err, ErrLossChanged) != c.changed || (!c.changed && err != nil) {
				t.Fatalf("moveAsRead = %v, %v; want moved %v, ErrLossChanged %v", moved, err, c.moved, c.changed)
			}
			if !moved {
				return
			}
			if _, err := os.Lstat(at); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the entry still stands in the copy after it moved: %v", err)
			}
			got, err := readCopyState(to)
			if err != nil {
				t.Fatal(err)
			}
			for p, s := range got {
				if w, ok := want[filepath.Join(at, p[len(to):])]; !ok || w != s {
					t.Fatalf("%s moved as %+v, but the copy held %+v there", p, s, w)
				}
			}
		})
	}

	t.Run("a name standing where it goes", func(t *testing.T) {
		t.Parallel()
		copyPath, aside := t.TempDir(), t.TempDir()
		at, to := filepath.Join(copyPath, "entry"), filepath.Join(aside, "entry")
		writeFile(t, at, "work\n")
		writeFile(t, to, "made there\n")
		before, err := readCopyState(copyPath)
		if err != nil {
			t.Fatal(err)
		}
		if moved, err := moveAsRead(at, to, before, "entry"); moved || err == nil || errors.Is(err, ErrLossChanged) {
			t.Fatalf("moveAsRead onto a standing name = %v, %v; want it refused, nothing moved", moved, err)
		}
		for p, content := range map[string]string{at: "work\n", to: "made there\n"} {
			if raw, err := os.ReadFile(p); err != nil || string(raw) != content {
				t.Fatalf("%s reads %q, %v; want %q", p, raw, err, content)
			}
		}
	})
}

// A directory lists as read only while it lists exactly the names read
// under it, whatever its stamp, so an entry added to or removed from it in
// the same timestamp tick as its last change is still seen. The positive
// control is the directory as read, and a file is not listed.
func TestADirectoryListsAsReadOnlyWithTheNamesReadUnderIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	for _, c := range []struct {
		name   string
		change func(t *testing.T, at string)
		want   bool
	}{
		{name: "as read", change: func(*testing.T, string) {}, want: true},
		{name: "an entry added", change: func(t *testing.T, at string) { writeFile(t, filepath.Join(at, "new.txt"), "added\n") }},
		{name: "an entry removed", change: func(t *testing.T, at string) {
			if err := os.Remove(filepath.Join(at, "a.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an entry renamed", change: func(t *testing.T, at string) {
			if err := os.Rename(filepath.Join(at, "a.txt"), filepath.Join(at, "z.txt")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			at := filepath.Join(t.TempDir(), "entry")
			tree(t, at)
			state, err := readCopyState(at)
			if err != nil {
				t.Fatal(err)
			}
			c.change(t, at)
			if got := listsAsRead(at, state, 2); got != c.want {
				t.Fatalf("listsAsRead = %v, want %v", got, c.want)
			}
			if !listsAsRead(filepath.Join(at, "sub", "b.txt"), state, 0) {
				t.Fatal("a file read as a file was listed")
			}
		})
	}
}

// file makes a regular file holding content.
func file(content string) func(t *testing.T, at string) {
	return func(t *testing.T, at string) { writeFile(t, at, content) }
}

// rewrite writes content over the file at at.
func rewrite(content string) func(t *testing.T, at string) {
	return file(content)
}

// tree makes a directory with a file and a subdirectory holding another.
func tree(t *testing.T, at string) {
	mkdirAll(t, filepath.Join(at, "sub"))
	writeFile(t, filepath.Join(at, "a.txt"), "a\n")
	writeFile(t, filepath.Join(at, "sub", "b.txt"), "b\n")
}

// link makes a symbolic link at at to a path that does not exist, which is
// moved as the link and never followed.
func link(t *testing.T, at string) {
	if err := os.Symlink(filepath.Join(t.TempDir(), "nowhere"), at); err != nil {
		t.Fatal(err)
	}
}
