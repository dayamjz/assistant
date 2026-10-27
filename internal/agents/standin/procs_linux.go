//go:build linux

package standin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processIDs is every process identifier /proc lists.
func processIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// commandLine reads pid's executable, the link /proc holds for it, and its
// arguments, argv[0] first, from its NUL-separated cmdline. Nothing is split
// on white space, so a path or an argument with a space in it is read whole.
func commandLine(pid int) (string, []string, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	executable, err := os.Readlink(dir + "/exe")
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return "", nil, err
	}
	return executable, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"), nil
}

// startEnvironment reads the environment the kernel holds for pid, one
// NAME=value string per variable, from its NUL-separated environ. That is the
// environment the process was started with, not one it set for itself
// later. Whether a given program's processes carry a variable there is a
// fact about that program, so what relies on it, the owner sweep, is tested
// against a real terminal server rather than against this comment.
func startEnvironment(pid int) ([]string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSuffix(string(raw), "\x00")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\x00"), nil
}

// foreignProcess reports whether pid runs as another user's: the owner of
// its /proc entry is not this process's effective user. It is read so that a
// listing passes over only the processes of another user's whose command
// line it could not read, rather than assuming that of every read that
// failed (processesWhere).
func foreignProcess(pid int) (bool, error) {
	info, err := os.Stat("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("process %d: /proc entry has no owner to read (%T)", pid, info.Sys())
	}
	return int(stat.Uid) != os.Geteuid(), nil
}

// pfExiting is PF_EXITING from the kernel's include/linux/sched.h, which
// proc(5) points the flags field of /proc/<pid>/stat at: the bit the
// kernel sets on a process as it begins to exit. A process carrying it
// does not return to user space, so it starts nothing.
const pfExiting = 0x00000004

// exitingProcess reports whether pid has begun to exit: pfExiting set in
// the flags field, the ninth, of /proc/<pid>/stat. It is asked only of a
// process whose read failed (processesWhere), for the case CI's ubuntu
// runner met (run 40211320712): a process of this user's that /proc still
// read as running while its exe link, gone with its address space, could
// not be read.
func exitingProcess(pid int) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false, err
	}
	return statExiting(string(raw))
}

// statExiting reads pfExiting from a /proc/<pid>/stat line: the fields after
// the command name, which is in parentheses and may hold spaces and
// parentheses of its own, so read after the last ')'; the state is field 3
// and the flags field 9.
func statExiting(stat string) (bool, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return false, fmt.Errorf("a /proc stat line has no command name: %q", stat)
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 7 {
		return false, fmt.Errorf("a /proc stat line has %d fields after its command name, want at least 7: %q", len(fields), stat)
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return false, fmt.Errorf("a /proc stat line's flags %q: %w", fields[6], err)
	}
	return flags&pfExiting != 0, nil
}
