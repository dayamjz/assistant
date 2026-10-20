package standin

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openTerminal opens a pseudo-terminal pair on macOS: the master through
// the cloning device, granted and unlocked with the two requests that take
// no argument, and the slave by the name the master's device number gives
// it, /dev/ttysNNN for minor number NNN, which is the name the system's own
// ptsname composes for a cloned master. The slave is opened without taking
// it as this process's controlling terminal; the child AtTerminal starts
// does that.
func openTerminal() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("granting the terminal: %w", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlocking the terminal: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("reading the terminal's device number: %w", err)
	}
	name := fmt.Sprintf("/dev/ttys%03d", unix.Minor(uint64(st.Rdev)))
	slave, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("opening the terminal's other side: %w", err)
	}
	return master, slave, nil
}
