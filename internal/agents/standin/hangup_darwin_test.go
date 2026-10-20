package standin

import "golang.org/x/sys/unix"

// ignoresHangup reports whether the kernel records pid as ignoring the
// hangup signal, read from its process table entry's ignored-signal mask,
// in which signal n is bit n-1.
func ignoresHangup(pid int) (bool, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return proc.Proc.P_sigignore&(1<<(unix.SIGHUP-1)) != 0, nil
}
