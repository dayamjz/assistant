//go:build linux || darwin

package ipc

import (
	"fmt"
	"net"
)

// rawControl runs f on the connection's file descriptor. peer_linux.go and
// peer_darwin.go share it to read a peer's credentials; peer_unsupported.go
// refuses instead and has no use for it.
func rawControl(uc *net.UnixConn, f func(fd uintptr)) error {
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("connection has no file descriptor: %w", err)
	}
	if err := raw.Control(f); err != nil {
		return fmt.Errorf("connection file descriptor is unusable: %w", err)
	}
	return nil
}
