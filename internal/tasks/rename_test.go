package tasks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// renameNoReplace moves a file or a directory to a free name and refuses,
// moving nothing, to land on a name that stands: a file, and an empty
// directory, which a plain rename of a directory replaces. That is what
// lets a discard make its directory, set an entry aside or put one back
// without taking the place of anything made under its name since. The
// positive control is the move to a free name.
func TestARenameThatMustNotReplaceRefusesToLandOnAName(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this build has a rename that refuses to replace only on Linux and macOS")
	}
	dir := t.TempDir()
	from, taken, free := filepath.Join(dir, "from"), filepath.Join(dir, "taken"), filepath.Join(dir, "free")
	writeFile(t, from, "moved\n")
	writeFile(t, taken, "made under the name\n")
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

	stage, empty := filepath.Join(dir, "stage"), filepath.Join(dir, "empty")
	mkdirAll(t, stage)
	writeFile(t, filepath.Join(stage, journalName), "{}")
	mkdirAll(t, empty)
	if err := renameNoReplace(stage, empty); err == nil {
		t.Fatal("the rename replaced the empty directory standing under the name")
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("the empty directory reads %v, %v; want it as it was made", entries, err)
	}
	if _, err := os.Stat(filepath.Join(stage, journalName)); err != nil {
		t.Fatalf("the refused rename moved the directory: %v", err)
	}
}
