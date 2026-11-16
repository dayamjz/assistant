//go:build linux || windows

package ipc

import "syscall"

// maxSocketPath is the whole address field: on these platforms Go's syscall
// package takes a path that fills it.
const maxSocketPath = len(syscall.RawSockaddrUnix{}.Path)
