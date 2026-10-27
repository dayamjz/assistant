//go:build unix

package vcs

import (
	"os"
	"syscall"
)

// openForDigest opens full for reading and returns without waiting for a
// writer. A FIFO with no writer would otherwise block in the open, and that
// wait would not observe the caller's context. The caller classifies the
// descriptor and does not read one that is not a regular file.
func openForDigest(full string) (*os.File, error) {
	return os.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
