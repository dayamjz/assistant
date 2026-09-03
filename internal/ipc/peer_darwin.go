package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCredentials reads the credentials the kernel recorded for the peer when
// the socket was connected. It takes two reads because this platform reports
// the user and group through one option and the process identifier through
// another, and both are needed before anything may be decided on them.
func peerCredentials(uc *net.UnixConn) (Credentials, error) {
	var (
		xucred *unix.Xucred
		pid    int
		credEr error
		pidErr error
	)
	if err := rawControl(uc, func(fd uintptr) {
		xucred, credEr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		pid, pidErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return Credentials{}, err
	}
	if credEr != nil {
		return Credentials{}, fmt.Errorf("LOCAL_PEERCRED: %w", credEr)
	}
	if pidErr != nil {
		return Credentials{}, fmt.Errorf("LOCAL_PEERPID: %w", pidErr)
	}
	creds := Credentials{PID: pid, UID: int(xucred.Uid)}
	if xucred.Ngroups > 0 {
		creds.GID = int(xucred.Groups[0])
	}
	return creds, nil
}
