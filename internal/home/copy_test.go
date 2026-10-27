package home_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// TestCopyAtAcceptsOnlyThisHomesCopiesWhereTheyStand holds the scope the
// sweep is given: a task copy and a run copy of this home, standing where
// the layout puts them, are accepted; the same task's copy under another
// home, the root, a task directory, a deeper path, a copy whose identifier
// the rule refuses, a relative path, a copy that does not exist, and a task
// copy path that is a symbolic link to another copy of this home are each
// refused with ErrNotACopy. The link is the case a lexical check alone would
// pass, which is why the control beside it is the copy it points at.
func TestCopyAtAcceptsOnlyThisHomesCopiesWhereTheyStand(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(filepath.Join(base, "home"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := home.Open(filepath.Join(base, "other"))
	if err != nil {
		t.Fatal(err)
	}
	mkdir := func(dir string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	taskCopy, _ := h.TaskCopy(id)
	mkdir(taskCopy)
	runCopy := mkdir(h.Worktree("repository", "run"))
	elsewhere, _ := other.TaskCopy(id)
	mkdir(elsewhere)
	linkID, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	linked, _ := h.TaskCopy(linkID)
	mkdir(filepath.Dir(linked))
	refused := []string{
		elsewhere, h.Root(), filepath.Dir(taskCopy), "tasks/" + id + "/copy",
		filepath.Join(h.Root(), "worktrees", "repository"),
	}
	if err := os.Symlink(taskCopy, linked); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		// Creating a link can need a privilege Windows withholds, and then
		// the linked copy's refusal is not established here.
		t.Logf("a symbolic link could not be created, so a linked copy's refusal is not checked on this platform: %v", err)
	} else {
		refused = append(refused, linked)
	}
	missingID, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	missing, _ := h.TaskCopy(missingID)
	badID := mkdir(filepath.Join(h.Root(), "tasks", "NOT-AN-ID", "copy"))
	deeper := mkdir(filepath.Join(taskCopy, "sub"))

	for _, dir := range []string{taskCopy, runCopy} {
		got, err := h.CopyAt(dir)
		if err != nil || got != dir {
			t.Fatalf("CopyAt(%s) = %q, %v, want it accepted as itself", dir, got, err)
		}
	}
	for _, dir := range append(refused, deeper, badID, missing) {
		if got, err := h.CopyAt(dir); !errors.Is(err, home.ErrNotACopy) {
			t.Fatalf("CopyAt(%s) = %q, %v, want ErrNotACopy", dir, got, err)
		}
	}
}

// TestDiscardingStandsBesideACopyByItsPathAlone holds where a confirmed
// discard keeps what it sets aside, PRD section 8's on-disk layout: beside
// a task's copy at tasks/<id>/.copy.discarding and beside a run's copy at
// worktrees/<repo>/.<run>.discarding. It is named from the copy's path
// whether or not the copy stands, since a discard past its point of no
// return has moved the copy away, and a path that is not where this home
// places a copy is refused with ErrNotACopy, as CopyAt refuses it.
func TestDiscardingStandsBesideACopyByItsPathAlone(t *testing.T) {
	h, err := home.Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	taskCopy, _ := h.TaskCopy(id)
	runCopy := h.Worktree("repository", "run")
	for copyPath, want := range map[string]string{
		taskCopy: filepath.Join(h.Root(), "tasks", id, ".copy.discarding"),
		runCopy:  filepath.Join(h.Root(), "worktrees", "repository", ".run.discarding"),
	} {
		if _, err := os.Lstat(copyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the copy at %s stands (%v); this case is one that does not", copyPath, err)
		}
		if got, err := h.Discarding(copyPath); err != nil || got != want {
			t.Fatalf("Discarding(%s) = %q, %v, want %q", copyPath, got, err, want)
		}
	}
	for _, dir := range []string{
		h.Root(), filepath.Dir(taskCopy), filepath.Join(taskCopy, "sub"), "tasks/" + id + "/copy",
		filepath.Join(h.Root(), "worktrees", "repository"), filepath.Join(h.Root(), "tasks", "NOT-AN-ID", "copy"),
	} {
		if got, err := h.Discarding(dir); !errors.Is(err, home.ErrNotACopy) {
			t.Fatalf("Discarding(%s) = %q, %v, want ErrNotACopy", dir, got, err)
		}
	}
}
