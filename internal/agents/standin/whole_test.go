package standin

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWhole puts all of its data under the name it is given, replaces a
// file already there, and leaves nothing else in the directory, so a reader
// selecting files by name finds only the finished record. A write into a
// directory that does not exist is refused and creates nothing, the control
// that the success above was the write's doing.
func TestWriteWholeLeavesOnlyTheFinishedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record")
	for _, want := range []string{"first record", "second"} {
		if err := writeWhole(path, []byte(want)); err != nil {
			t.Fatalf("writeWhole(%q): %v", want, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("reading back %q: %q, %v", want, got, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "record" {
			t.Fatalf("the directory holds %v after writing %q; want only the record", entries, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("the record's mode is %v; want 0600", perm)
		}
	}

	missing := filepath.Join(dir, "absent", "record")
	if err := writeWhole(missing, []byte("x")); err == nil {
		t.Fatal("writeWhole into a missing directory succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a refused write left %s: %v", missing, err)
	}
}

// Once writeWhole has written its new record and before it renames it into
// place, the destination either does not exist or still holds the previous
// whole record, and the whole new record is already under a different name
// beside it. The rename is therefore the only step that changes what a
// reader of the destination sees, so a reader polling it cannot read a
// partial record however it interleaves with the writer (#459). The seam
// that observes that moment must be reached once per write, so a writeWhole
// that writes the destination in place, and so never reaches it, fails here
// rather than passing by observing nothing. The test sets a package-level
// seam, so it is not parallel.
func TestWriteWholeNeverShowsTheDestinationPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record")
	var previous *string
	var writing string
	reached := 0
	hook := func(temp, dest string) {
		if dest != path {
			return
		}
		reached++
		got, err := os.ReadFile(dest)
		switch {
		case previous == nil && !os.IsNotExist(err):
			t.Errorf("before the rename the destination exists: %q, %v; want no file", got, err)
		case previous != nil && (err != nil || string(got) != *previous):
			t.Errorf("before the rename the destination holds %q, %v; want the previous record %q", got, err, *previous)
		}
		if filepath.Dir(temp) != dir || temp == dest {
			t.Errorf("the temporary file %s is not a different name beside %s", temp, dest)
		}
		if got, err := os.ReadFile(temp); err != nil || string(got) != writing {
			t.Errorf("before the rename the temporary file holds %q, %v; want the whole new record %q", got, err, writing)
		}
	}
	beforeWholeRename.Store(&hook)
	t.Cleanup(func() { beforeWholeRename.Store(nil) })

	for i, want := range []string{"first record", "second"} {
		writing = want
		if err := writeWhole(path, []byte(want)); err != nil {
			t.Fatalf("writeWhole(%q): %v", want, err)
		}
		if reached != i+1 {
			t.Fatalf("writeWhole(%q) reached the point between its write and its rename %d times in all, want %d", want, reached, i+1)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("reading back %q: %q, %v", want, got, err)
		}
		previous = &want
	}
}
