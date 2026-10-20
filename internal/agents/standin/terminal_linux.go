package standin

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openTerminal opens a pseudo-terminal pair on Linux: the master through
// the cloning device, unlocked, and the slave by the number the master
// reports, /dev/pts/N. The slave is opened without taking it as this
// process's controlling terminal; the child AtTerminal starts does that.
func openTerminal() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlocking the terminal: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("reading the terminal's number: %w", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("opening the terminal's other side: %w", err)
	}
	return master, slave, nil
}
