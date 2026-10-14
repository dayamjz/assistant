package agents

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
	fields, found, err := statFields(pid)
	if err != nil || !found {
		return 0, false, err
	}
	if state := fields[0]; state == "Z" || state == "X" {
		return 0, false, nil
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("agents: /proc/%d/stat start time %q: %w", pid, fields[19], err)
	}
	return start, true, nil
}

// statFields reads /proc/<pid>/stat and returns its fields from field 3, the
// state, onward, so fields[0] is field 3 and field n is fields[n-3]. found is
// false when no process has the identifier, which proves absence. The command
// name is in parentheses and may contain spaces and parentheses of its own,
// so the fields are read after the last ')'; an entry with fewer than the 20
// fields startTime reads is refused with errStatShape rather than indexed.
func statFields(pid int) ([]string, bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return nil, false, nil
		}
		return nil, false, err
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return nil, false, fmt.Errorf("%w: /proc/%d/stat has no command name", errStatShape, pid)
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 20 {
		return nil, false, fmt.Errorf("%w: /proc/%d/stat has %d fields after the command name, want at least 20", errStatShape, pid, len(fields))
	}
	return fields, true, nil
}
