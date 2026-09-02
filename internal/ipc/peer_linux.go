package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCredentials reads the credentials the kernel recorded for the peer when
// the socket was connected. The kernel is the source, so a peer cannot state
// anything about itself here.
func peerCredentials(uc *net.UnixConn) (Credentials, error) {
	var (
		ucred  *unix.Ucred
		sysErr error
	)
	if err := rawControl(uc, func(fd uintptr) {
		ucred, sysErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Credentials{}, err
	}
	if sysErr != nil {
		return Credentials{}, fmt.Errorf("SO_PEERCRED: %w", sysErr)
	}
	return Credentials{PID: int(ucred.Pid), UID: int(ucred.Uid), GID: int(ucred.Gid)}, nil
}
