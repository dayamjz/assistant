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
	return startOf(pid, fields)
}

// startOf is startTime's reading of fields statFields or statEntry returned.
func startOf(pid int, fields []string) (uint64, bool, error) {
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
	_, fields, found, err := statEntry(pid)
	return fields, found, err
}

// statEntry is statFields with field 2, the command name, returned beside
// the fields: the text between the first '(' and the last ')' of the same
// read. An entry with no '(' before that ')' is refused with errStatShape.
func statEntry(pid int) (name string, fields []string, found bool, err error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return "", nil, false, fmt.Errorf("%w: /proc/%d/stat has no command name", errStatShape, pid)
	}
	open := strings.IndexByte(text, '(')
	if open < 0 || open > end {
		return "", nil, false, fmt.Errorf("%w: /proc/%d/stat has no '(' opening its command name", errStatShape, pid)
	}
	fields = strings.Fields(text[end+1:])
	if len(fields) < 20 {
		return "", nil, false, fmt.Errorf("%w: /proc/%d/stat has %d fields after the command name, want at least 20", errStatShape, pid, len(fields))
	}
	return text[open+1 : end], fields, true, nil
}

// commandName reads the command name of the process id names out of the
// same read of /proc/<pid>/stat as the start time it is checked against, so
// a name returned without an error came from an entry whose start time is
// id's. It is for diagnosis only: nothing decides from it.
func commandName(id Identity) (string, error) {
	name, fields, found, err := statEntry(id.PID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errNameGone
	}
	start, running, err := startOf(id.PID, fields)
	switch {
	case err != nil:
		return "", err
	case !running:
		return "", errNameGone
	case start != id.Start:
		return "", errNameReplaced
	}
	return name, nil
}

// lineage reads the parent and the process group from the same stat entry
// startTime reads: field 4 and field 5, after the command name.
func lineage(pid int) (Lineage, bool, error) {
	fields, found, err := statFields(pid)
	if err != nil || !found {
		return Lineage{}, false, err
	}
	start, running, err := startOf(pid, fields)
	if err != nil || !running {
		return Lineage{}, false, err
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return Lineage{}, false, fmt.Errorf("agents: /proc/%d/stat parent %q: %w", pid, fields[1], err)
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return Lineage{}, false, fmt.Errorf("agents: /proc/%d/stat process group %q: %w", pid, fields[2], err)
	}
	return Lineage{Identity: Identity{PID: pid, Start: start}, Parent: parent, Group: group}, true, nil
}
