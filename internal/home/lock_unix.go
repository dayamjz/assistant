//go:build unix

package home

import (
	"errors"
	"os"
	"syscall"
)

// tryLock asks for an exclusive advisory lock on the whole file without
// waiting, and reports whether it was taken. A lock another process holds is
// not an error here: it is the answer, and the caller decides whether to wait
// for it or to refuse.
func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

// unlock releases the lock. Closing the descriptor would release it too, and
// this is done first so the release is a call that can report a failure rather
// than a side effect of a close.
func unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
