//go:build darwin

package standin

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
// first, from the kernel's kern.procargs2 record (procArgs). Nothing is
// split on white space, so a path or an argument with a space in it is read
// whole.
func commandLine(pid int) (string, []string, error) {
	executable, args, _, err := procArgs(pid)
	return executable, args, err
}

// startEnvironment reads the environment the kernel holds for pid, one
// NAME=value string per variable, from the same record. That is the
// environment the process was started with, not one it set for itself
// later. Whether a given program's processes carry a variable there is a
// fact about that program, so what relies on it, the owner sweep, is tested
// against a real terminal server rather than against this comment.
func startEnvironment(pid int) ([]string, error) {
	_, _, env, err := procArgs(pid)
	return env, err
}

// procArgs parses pid's kern.procargs2 record: a 32-bit argument count, the
// path the process was executed by and its terminating NUL, padding NULs,
// then that many NUL-terminated arguments, then the NUL-terminated
// environment strings up to the first empty one, after which come the
// strings the kernel adds for the loader, which are not the environment.
func procArgs(pid int) (string, []string, []string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, nil, err
	}
	if len(raw) < 4 {
		return "", nil, nil, fmt.Errorf("process %d: kern.procargs2 is %d bytes", pid, len(raw))
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil, nil, fmt.Errorf("process %d: kern.procargs2 has no terminated executable path", pid)
	}
	executable := string(rest[:end])
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", nil, nil, errors.New("kern.procargs2 ends inside an argument")
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	var env []string
	for {
		end := bytes.IndexByte(rest, 0)
		if end <= 0 {
			break
		}
		env = append(env, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, args, env, nil
}
