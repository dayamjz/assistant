package session

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// startTime reads field 22 of /proc/<pid>/stat, the time the process started
// after boot in clock ticks, which is stable for the process's life and
// differs for any process later given the same identifier within one boot.
// A process in state Z or X has exited and is counted as not running, so a
// zombie nobody has reaped yet proves absence rather than presence.
func startTime(pid int) (uint64, bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return 0, false, nil
		}
		return 0, false, err
	}
	// The command name is in parentheses and may contain spaces and
	// parentheses of its own, so the fields are read after the last ')'.
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, false, fmt.Errorf("session: /proc/%d/stat has no command name", pid)
	}
	fields := strings.Fields(text[end+1:])
	// fields[0] is field 3, the state; field 22 is therefore fields[19].
	if len(fields) < 20 {
		return 0, false, fmt.Errorf("session: /proc/%d/stat has %d fields after the command name, want at least 20", pid, len(fields))
	}
	if state := fields[0]; state == "Z" || state == "X" {
		return 0, false, nil
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("session: /proc/%d/stat start time %q: %w", pid, fields[19], err)
	}
	return start, true, nil
}
