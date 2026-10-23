package procgroup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// WriteProgram writes content to path with mode perm, as a program this
// process or another is about to start, so that no process this one starts
// is left holding the file open for writing. It is the one way this module
// writes a file that something then executes; internal/procgroup/guard
// fails the build on a write with an execute bit anywhere else.
//
// execve(2) refuses, with ETXTBSY, a file that any process holds open for
// writing. A descriptor this process opens is closed on exec, but a child
// forked while it is open holds a copy of it until that child itself execs,
// so a write that races another goroutine's start can leave the written
// program unstartable for that moment: os.WriteFile followed by a start is
// that race, and #539 is a test that lost it. WriteProgram opens, writes and
// closes the file while holding syscall.ForkLock for reading. That lock's
// documented contract, on the platforms that fork, is that the fork of
// every start through package syscall, which os/exec and os.StartProcess
// use, holds it for writing, so
// no such start forks while the file is open and no child of this process
// can hold the descriptor. It fixes the order rather than retrying a
// refused start.
//
// The content is written to a file beside path that is renamed over it once
// closed, so a program already at path is replaced whole rather than
// rewritten in place, and a start that reads path finds the old program or
// the new one, never part of either. perm is set as given, not reduced by
// the umask. On an error, path keeps what it held before, and the file
// beside it is removed.
//
// What it does not cover:
//
//   - A fork that does not go through package syscall, such as one in cgo,
//     and a descriptor any other process opens: the lock reaches neither.
//   - A program something else writes, such as a script a child shell
//     writes and then runs.
//   - A process killed between the write and the rename leaves the file
//     beside path, named after path's base with a leading dot.
//
// While it writes, every start in this process waits for it, so a large
// program delays those starts by the time its write takes. The write must
// not itself start a program, which would wait on the lock it holds.
func WriteProgram(path string, content []byte, perm fs.FileMode) error {
	return writeProgram(path, perm, func(f *os.File) error {
		_, err := f.Write(content)
		return err
	})
}

// writeProgram is WriteProgram with the write as a function of the open
// file, which a test uses to look at the lock while the file is open.
func writeProgram(path string, perm fs.FileMode, write func(*os.File) error) error {
	staged, err := stageProgram(path, perm, write)
	if err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("procgroup: putting the program in place at %s: %w", path, err)
	}
	return nil
}

// stageProgram writes the program to a new file beside path, under
// syscall.ForkLock held for reading from before the file is opened until
// after it is closed, and returns the new file's name.
func stageProgram(path string, perm fs.FileMode, write func(*os.File) error) (string, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", fmt.Errorf("procgroup: writing the program %s: %w", path, err)
	}
	name := f.Name()
	err = write(f)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("procgroup: writing the program %s: %w", path, err)
	}
	return name, nil
}
