//go:build linux || darwin

package procgroup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestNoStartForksWhileAProgramIsOpenForWriting is #539's ordering, asked of
// the lock rather than of a race: every start through package syscall forks
// holding syscall.ForkLock for writing, so a write lock that cannot be taken
// for the whole time the program's file is open is a fork that cannot happen
// then, and a child that cannot hold the file open. The lock is free before
// the write and after it, which is the positive control: a lock held by
// something else would make the check inside the write pass by looking at
// nothing. Before WriteProgram took the lock, the write lock was free while
// the file was open, and a test that wrote its stand-in tmux and started it
// was refused with ETXTBSY whenever a parallel test forked inside that window.
func TestNoStartForksWhileAProgramIsOpenForWriting(t *testing.T) {
	requireForkLockFree(t, "before the write")
	path := filepath.Join(t.TempDir(), "program")
	opened := false
	err := writeProgram(path, 0o755, func(f *os.File) error {
		opened = true
		if syscall.ForkLock.TryLock() {
			syscall.ForkLock.Unlock()
			t.Error("a fork could take syscall.ForkLock while the program was open for writing")
		}
		_, err := f.WriteString("#!/bin/sh\nexit 0\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("the write never ran")
	}
	requireForkLockFree(t, "after the write")
}

func requireForkLockFree(t *testing.T, when string) {
	t.Helper()
	if !syscall.ForkLock.TryLock() {
		t.Fatalf("syscall.ForkLock was held %s, so the check while the program is open would prove nothing", when)
	}
	syscall.ForkLock.Unlock()
}

// TestAProgramIsWrittenWholeWithItsMode holds WriteProgram to its contract
// for what lands at path: the content, the mode as given whatever the umask,
// a program already there replaced by a new file rather than rewritten in
// place, and nothing left beside it.
func TestAProgramIsWrittenWholeWithItsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "program")
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	if err := WriteProgram(path, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := requireProgram(t, path, "first", 0o755)
	if err := WriteProgram(path, []byte("second"), 0o750); err != nil {
		t.Fatal(err)
	}
	second := requireProgram(t, path, "second", 0o750)
	if os.SameFile(first, second) {
		t.Error("the second write rewrote the first program's file in place rather than replacing it")
	}
	requireOnly(t, dir, "program")
}

// TestAFailedWriteLeavesThePathAsItWas is the error half: a write that fails
// part way leaves the program already at path as it was, and removes the
// file it was writing.
func TestAFailedWriteLeavesThePathAsItWas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "program")
	if err := WriteProgram(path, []byte("kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	err := writeProgram(path, 0o755, func(f *os.File) error {
		_, _ = f.WriteString("half")
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("writeProgram answered %v, want the write's own error", err)
	}
	requireProgram(t, path, "kept", 0o755)
	requireOnly(t, dir, "program")
	if err := WriteProgram(filepath.Join(dir, "missing", "program"), []byte("x"), 0o755); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write into a directory that does not exist answered %v, want os.ErrNotExist", err)
	}
}

func requireProgram(t *testing.T, path, content string, perm os.FileMode) os.FileInfo {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("%s holds %q, want %q", path, got, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != perm {
		t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), perm)
	}
	return info
}

func requireOnly(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s holds %v, want only %s", dir, names, name)
	}
}
