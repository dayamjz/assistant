//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package standin

import "golang.org/x/sys/unix"

// makeRaw puts the terminal on fd in raw mode, the mode the Claude Code CLI
// reads its terminal in: no line editing, no echo, no signal keys, and no
// translation of a carriage return, so every key arrives as the byte it is.
// It returns what puts the terminal back.
func makeRaw(fd int) (func(), error) {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	old := *t
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, &old) }, nil
}
