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

// A discard that stopped during its first journal write leaves only its
// staging directory (home's DiscardingStage) holding a partial file of the
// journal, the state writeWhole leaves between its write and its rename,
// and nothing at the discard's directory's name, since that is made only
// by renaming a staging directory with a whole journal in it. So the copy
// is not blocked: standingDiscard finds nothing standing and the next
// discard begins, in a staging directory of its own. The stopped one's is
// left exactly as it was, since nothing in it proves it this program's:
// the open finding a690cc38 began on feat/age-407-discard, where the
// leftover refused every later discard of the copy for good.
func TestADiscardStoppedInItsFirstJournalWriteBlocksNoLaterDiscard(t *testing.T) {
	principles.Cite(t, principles.P15)
	s, tk, _, _ := keptWithWork(t)
	parent, pattern, err := s.home.DiscardingStage(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	// The staging directory os.MkdirTemp makes from the pattern, made here
	// by name so the test does not call os.MkdirTemp (standin's rule): the
	// random part os.MkdirTemp writes in place of the pattern's "*".
	stage := filepath.Join(parent, strings.Replace(pattern, "*", "1027", 1))
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	partial, err := os.CreateTemp(stage, partialPrefix(filepath.Join(stage, journalName))+"*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.WriteString(`{"copy":"12:3`); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	stopped := treeAndGitState(t, stage)

	if _, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || found {
		t.Fatalf("standingDiscard beside a stopped first write = %v, %v; want nothing standing", found, err)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatalf("the next discard could not begin: %v", err)
	}
	if j, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || !found || j.Copy == "" {
		t.Fatalf("after the next discard began, standingDiscard = %+v, %v, %v; want its journal", j, found, err)
	}
	if dir == stage {
		t.Fatalf("the next discard took the stopped one's staging directory %s", stage)
	}
	if now := treeAndGitState(t, stage); now != stopped {
		t.Fatalf("the stopped discard's staging directory changed:\n%s\nwant\n%s", now, stopped)
	}
}

// Nothing at the discard's directory's name that a journal does not prove
// a discard's is ever removed (the reviews of d279357 and 3d8cac1): a
// directory with no journal, whatever it holds (a foreign file; a file or
// an empty directory named exactly as writeWhole names a partial journal),
// a journal that cannot be read, one that is null, names nothing or names
// no copy's identity, a journal that is a symbolic link, and a file where
// the directory goes are each refused with ErrAsideStands and left exactly
// as they were; and a discard does not begin over any of them. A partial
// file beside a whole journal is no reason to refuse: the journal is the
// last one written whole, and it is read.
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
		{"a file named like a partial journal and no journal", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, partialPrefix(filepath.Join(dir, journalName))+"-notes"), "someone's notes\n")
		}},
		{"a file named exactly like a partial journal and no journal", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, partialPrefix(filepath.Join(dir, journalName))+"20261027150000"), "recovery notes\n")
		}},
		{"an empty directory named like a partial journal and no journal", func(t *testing.T, dir string) {
			mkdirAll(t, filepath.Join(dir, partialPrefix(filepath.Join(dir, journalName))+"123"))
		}},
		{"a journal that cannot be read", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, journalName), "not a journal")
		}},
		{"a journal that is null", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, journalName), "null")
		}},
		{"a journal that names nothing", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, journalName), "{}")
		}},
		{"a journal that names no copy's identity", func(t *testing.T, dir string) {
			mkdirAll(t, dir)
			writeFile(t, filepath.Join(dir, journalName), `{"copy":"not-an-identity"}`)
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
			before := treeAndGitState(t, dir)
			if _, found, err := s.cleaner.standingDiscard(tk.copy); !errors.Is(err, ErrAsideStands) || found {
				t.Fatalf("standingDiscard = %v, %v; want ErrAsideStands", found, err)
			}
			if _, err := s.cleaner.beginDiscard(tk.copy); !errors.Is(err, ErrAsideStands) {
				t.Fatalf("beginDiscard over it = %v; want ErrAsideStands", err)
			}
			if now := treeAndGitState(t, dir); now != before {
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
