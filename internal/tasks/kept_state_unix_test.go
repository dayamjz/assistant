//go:build linux || darwin

package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// moveAsRead refuses what moved when it is seen to change while it is
// read (the review of 50277f10): the reviewer's 512 MiB file rewritten
// after its prefix was hashed was accepted unchanged. Through the seam, a
// moved file rewritten with other bytes of the same size once it is
// hashed, and a moved directory given an entry once it is listed, each
// refuse with ErrLossChanged, moved, holding the write where it moved for
// the caller to put back. The positive control is the same read with
// nothing written during it. Not parallel: it sets the package's seam.
func TestAnEntryChangedWhileItIsReadIsNotSetAsideAsRead(t *testing.T) {
	principles.Cite(t, principles.P6)
	cases := []struct {
		name    string
		make    func(t *testing.T, at string)
		during  func(t *testing.T, to string)
		changed bool
	}{
		{name: "nothing written during the read", make: file("work\n"), during: func(*testing.T, string) {}},
		{name: "a file rewritten during the read", make: file("work\n"), during: func(t *testing.T, to string) {
			afterLastChange(t, to)
			f, err := os.OpenFile(to, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			if _, err := f.WriteAt([]byte("W"), 0); err != nil {
				t.Fatal(err)
			}
		}, changed: true},
		{name: "a file added to a directory during the read", make: tree, during: func(t *testing.T, to string) {
			writeFile(t, filepath.Join(to, "sub", "new.txt"), "added\n")
		}, changed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			copyPath, aside := t.TempDir(), t.TempDir()
			at, to := filepath.Join(copyPath, "entry"), filepath.Join(aside, "entry")
			c.make(t, at)
			before, err := readCopyState(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			stateReadForTest = func() { c.during(t, to) }
			moved, err := moveAsRead(at, to, before, "entry")
			stateReadForTest = nil
			if !moved || errors.Is(err, ErrLossChanged) != c.changed || (!c.changed && err != nil) {
				t.Fatalf("moveAsRead = %v, %v; want moved, ErrLossChanged %v", moved, err, c.changed)
			}
			if _, err := os.Lstat(to); err != nil {
				t.Fatalf("what moved is not where it moved: %v", err)
			}
		})
	}
}

// afterLastChange returns once the file system stamps a change beside p
// with a later change time than p's last change, so a write to p from here
// on is the reviewer's, a tick after the move: one in the same tick as the
// move's rename is the residual kept_state.go names. It fails the test
// after five seconds.
func afterLastChange(t *testing.T, p string) {
	t.Helper()
	was, err := stampOf(p)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(filepath.Dir(p), ".tick")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		writeFile(t, probe, "tick\n")
		now, err := stampOf(probe)
		if err != nil {
			t.Fatal(err)
		}
		if now.ctime[0] > was.ctime[0] || (now.ctime[0] == was.ctime[0] && now.ctime[1] > was.ctime[1]) {
			return
		}
	}
	t.Fatalf("the file system stamped no change later than %s's", p)
}

// Roots that overlap read as the outer root alone does (the review of
// 9986941): an unchanged directory read with one of its own entries as a
// second root is no change, and its state is the outer root's. A change
// during the read is still seen through the overlap: a file added under
// the inner root refuses with ErrLossChanged. Not parallel: it sets the
// package's seam.
func TestOverlappingRootsReadAsTheOuterRootAlone(t *testing.T) {
	principles.Cite(t, principles.P6)
	dir := filepath.Join(t.TempDir(), "entry")
	tree(t, dir)
	inner := filepath.Join(dir, "sub")
	alone, err := readCopyState(dir)
	if err != nil {
		t.Fatal(err)
	}
	both, err := readCopyState(dir, inner)
	if err != nil {
		t.Fatalf("reading overlapping roots of an unchanged directory: %v", err)
	}
	if len(both) != len(alone) {
		t.Fatalf("overlapping roots read %d entries, the outer root alone %d", len(both), len(alone))
	}
	for p, s := range alone {
		if both[p] != s {
			t.Fatalf("%s read as %+v with overlapping roots, %+v alone", p, both[p], s)
		}
	}

	stateReadForTest = func() { writeFile(t, filepath.Join(inner, "new.txt"), "added\n") }
	_, err = readCopyState(dir, inner)
	stateReadForTest = nil
	if !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a file added under the inner root during the read = %v; want ErrLossChanged", err)
	}
}
