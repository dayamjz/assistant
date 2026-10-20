//go:build darwin

package cli_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// processIDs is every process identifier in the kernel's process table.
func processIDs() ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, int(p.Proc.P_pid))
	}
	return pids, nil
}

// commandLine reads pid's executable path and its arguments, argv[0]
// first, from the kernel's kern.procargs2 record: a 32-bit argument count,
// the path the process was executed by and its terminating NUL, padding
// NULs, and then that many NUL-terminated arguments. Nothing is split on
// white space, so a path or an argument with a space in it is read whole.
func commandLine(pid int) (string, []string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, err
	}
	if len(raw) < 4 {
		return "", nil, fmt.Errorf("process %d: kern.procargs2 is %d bytes", pid, len(raw))
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil, fmt.Errorf("process %d: kern.procargs2 has no terminated executable path", pid)
	}
	executable := string(rest[:end])
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", nil, errors.New("kern.procargs2 ends inside an argument")
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, args, nil
}
