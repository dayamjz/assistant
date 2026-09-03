//go:build !linux && !darwin

package ipc

import (
	"errors"
	"net"
	"runtime"
)

// peerCredentials refuses, because this platform has no way to ask the kernel
// who opened a local socket. The refusal is the whole answer: identification
// is authority, so a build here serves what needs none and refuses the rest
// rather than inventing an identity to let a decision through.
func peerCredentials(*net.UnixConn) (Credentials, error) {
	return Credentials{}, errors.New(runtime.GOOS + " does not report local socket peer credentials")
}
