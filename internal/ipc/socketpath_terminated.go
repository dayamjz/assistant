//go:build !linux && !windows

package ipc

import "syscall"

// maxSocketPath is one byte short of the address field: on these platforms
// Go's syscall package keeps a byte of it for the path's terminator.
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1
