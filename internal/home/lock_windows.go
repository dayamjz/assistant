//go:build windows

package home

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockLength is how much of the lock file is locked. The file holds nothing,
// so one byte is the whole of what a lock has to cover, and a fixed region is
// what makes two processes ask about the same thing.
const lockLength = 1

// tryLock asks for an exclusive lock on the file's first byte without waiting,
// and reports whether it was taken. A region another process holds comes back
// as a refusal to wait rather than as a failure, which is the answer this
// returns false for.
func tryLock(file *os.File) (bool, error) {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockLength, 0, overlapped)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

// unlock releases the locked region.
func unlock(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, lockLength, 0, overlapped)
}
