//go:build linux

package cli_test

import (
	"os"
	"strconv"
	"strings"
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
