//go:build unix

package gate

import (
	"errors"
	"os"
	"syscall"
)

// tryLockCopies asks for the copies lock without waiting and reports whether
// it was taken. A lock another holder has is the answer false, not a failure.
func tryLockCopies(file *os.File, exclusive bool) (bool, error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	err := syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

// unlockCopies releases the copies lock before the descriptor is closed, so
// the release is a call that can report a failure.
func unlockCopies(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
