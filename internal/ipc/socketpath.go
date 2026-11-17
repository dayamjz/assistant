package ipc

import (
	"errors"
	"fmt"
	"syscall"
)

// ErrSocketPathTooLong is CheckSocketPath's refusal: the socket path is
// longer than MaxSocketPath, so no local socket can be bound or dialed at it
// on this platform. It is refused here, naming the path, its length and the
// limit, rather than left to the bind or the dial, whose error is an
// unexplained "invalid argument".
var ErrSocketPathTooLong = errors.New("ipc: the socket path is too long for a local socket")

// MaxSocketPath is the longest socket path, in bytes, that this package lets
// a local socket be bound or dialed at on this platform: one byte short of
// the address field Go's syscall package holds the path in, the byte its
// terminator takes, so 103 on macOS and 107 on Linux.
// TestMaxSocketPathIsTheLongestPathThatBinds checks on each platform CI runs
// that a path this long binds and one byte longer does not.
const MaxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// CheckSocketPath refuses a socket path no local socket can use on this
// platform with ErrSocketPathTooLong, and returns nil for any other path. It
// checks the length alone: whether the path's directory exists, or whether
// a bind or dial there would then succeed, is the bind's or the dial's.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf("%w: %s is %d bytes, and this platform allows at most %d", ErrSocketPathTooLong, path, len(path), MaxSocketPath)
	}
	return nil
}
