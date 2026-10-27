package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A confirmed discard's directory stands beside the copy, home's
// Discarding, and its first journal names the copy's identity before
// anything is set aside (PRD section 6, "Removing a discarded copy", step
// 2). standingDiscard reads it back. A second discard's directory is
// refused while one stands, with ErrAsideStands, and the one standing is
// left exactly as it was.
func TestADiscardsDirectoryIsMadeBesideItsCopyWithAJournalNamingIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)

	if _, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || found {
		t.Fatalf("before any discard, standingDiscard = %v, %v; want nothing standing", found, err)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := s.home.Discarding(tk.copy)
	if dir != want || filepath.Dir(dir) != filepath.Dir(tk.copy) {
		t.Fatalf("the discard's directory is %s, want %s beside the copy", dir, want)
	}
	j, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("after beginDiscard, standingDiscard = %+v, %v, %v; want the journal", j, found, err)
	}
	id, err := IdentifyCopy(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := ParseCopyIdentity(j.Copy)
	if err != nil || !recorded.Same(id) {
		t.Fatalf("the journal names the copy %q (%v), want %s", j.Copy, err, id)
	}

	before := treeAndGitState(t, dir)
	if _, err := s.cleaner.beginDiscard(tk.copy); !errors.Is(err, ErrAsideStands) {
		t.Fatalf("a second beginDiscard = %v, want ErrAsideStands", err)
	}
	if now := treeAndGitState(t, dir); now != before {
		t.Fatalf("the refused beginDiscard changed the directory standing:\n%s\nwant\n%s", now, before)
	}
}

// A discard that stopped during its first journal write leaves its
// directory holding a partial file of the journal and nothing else, the
// state writeWhole leaves between its write and its rename. Nothing was set
// aside then, so standingDiscard clears the partial file and the
// directory, and the next discard begins: the open finding a690cc38 began
// on feat/age-407-discard. With the clearing removed the leftover refuses
// every later discard of the copy for good.
func TestAPartialFirstJournalIsClearedAndTheNextDiscardBegins(t *testing.T) {
	principles.Cite(t, principles.P15)
	s, tk, _, _ := keptWithWork(t)
	dir, _ := s.home.Discarding(tk.copy)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	partial, err := os.CreateTemp(dir, partialPrefix(filepath.Join(dir, journalName))+"*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.WriteString(`{"copy":"12:3`); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}

	if _, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || found {
		t.Fatalf("standingDiscard over a partial first journal = %v, %v; want it cleared", found, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discard's directory still stands after it was cleared: %v", err)
	}
	if _, err := s.cleaner.beginDiscard(tk.copy); err != nil {
		t.Fatalf("the next discard could not begin: %v", err)
	}
}

// What a discard's directory holds that no discard writes there is never
// removed: a directory with no journal holding any other file, a journal
// that cannot be read, a journal that is a symbolic link, and a file where
// the directory goes are each refused with ErrAsideStands and left exactly
// as they were. A partial file beside a whole journal is no reason to
// refuse: the journal is the last one written whole, and it is read.
func TestADiscardsDirectoryItCannotTakeUpIsLeftAndRefused(t *testing.T) {
	principles.Cite(t, principles.P6)
	cases := []struct {
		name string
		make func(t *testing.T, dir string)
	}{
		{"a foreign file and no journal", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, "work.txt"), "someone's file\n")
		}},
		{"a journal that cannot be read", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, journalName), "not a journal")
		}},
		{"a journal that is a symbolic link", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			target := filepath.Join(t.TempDir(), "elsewhere.json")
			writeFile(t, target, `{"copy":"1:2:3:4"}`)
			if err := os.Symlink(target, filepath.Join(dir, journalName)); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("a symbolic link could not be created: %v", err)
				}
				t.Fatal(err)
			}
		}},
		{"a file where the directory goes", func(t *testing.T, dir string) {
			writeFile(t, dir, "a file\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, tk, _, _ := keptWithWork(t)
			dir, _ := s.home.Discarding(tk.copy)
			c.make(t, dir)
			before := treeAndGitState(t, filepath.Dir(dir))
			if _, found, err := s.cleaner.standingDiscard(tk.copy); !errors.Is(err, ErrAsideStands) || found {
				t.Fatalf("standingDiscard = %v, %v; want ErrAsideStands", found, err)
			}
			if now := treeAndGitState(t, filepath.Dir(dir)); now != before {
				t.Fatalf("the refusal changed what stands:\n%s\nwant\n%s", now, before)
			}
		})
	}

	t.Run("a partial file beside a whole journal", func(t *testing.T) {
		s, tk, _, _ := keptWithWork(t)
		dir, err := s.cleaner.beginDiscard(tk.copy)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, partialPrefix(filepath.Join(dir, journalName))+"77"), `{"copy":"`)
		if j, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || !found || j.Copy == "" {
			t.Fatalf("standingDiscard = %+v, %v, %v; want the whole journal read", j, found, err)
		}
	})
}

// mkdirAll makes dir and its parents.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// writeFile writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
