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
