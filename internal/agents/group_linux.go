package agents

import (
	"errors"
	"os"
	"strconv"
)

// exitedOnly lists the group's members from /proc and reports whether at
// least one was listed and every one listed is in state Z or X, which is a
// process that has exited and not been reaped. A failure to list or to read
// an entry answers no, so an unreadable listing never reads as an exited
// group; an entry that vanished between the listing and the read is a
// member that was reaped meanwhile and is skipped.
func exitedOnly(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	listed := false
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fields, found, err := statFields(pid)
		switch {
		case err != nil:
			return false
		case !found:
			continue
		}
		// fields[0] is field 3, the state, and fields[2] is field 5, the
		// process group.
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return false
		}
		if group != pgid {
			continue
		}
		listed = true
		if state := fields[0]; state != "Z" && state != "X" {
			return false
		}
	}
	return listed
}

// errStatShape is statFields' refusal of a stat entry it cannot read fields
// out of.
var errStatShape = errors.New("agents: a /proc stat entry has an unexpected shape")
