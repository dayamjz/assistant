//go:build windows

package gate

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// copiesLockLength is how much of the lock file is locked: one byte, at a
// fixed place, so every holder asks about the same region.
const copiesLockLength = 1

// tryLockCopies asks for the copies lock without waiting and reports whether
// it was taken. A region another holder has is the answer false, not a
// failure.
func tryLockCopies(file *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, copiesLockLength, 0, new(windows.Overlapped))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

// unlockCopies releases the locked region.
func unlockCopies(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, copiesLockLength, 0, new(windows.Overlapped))
}
