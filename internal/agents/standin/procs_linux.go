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
