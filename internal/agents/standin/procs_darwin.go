//go:build darwin

package standin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// processList is every process in the kernel's process table, each with the
// parent the same reading named (Eproc.Ppid) and its start time, so a
// listing that meets the process gone still has the parent that reading
// held (processesWhere, #722).
func processList() ([]listedProcess, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	list := make([]listedProcess, 0, len(procs))
	for _, p := range procs {
		t := p.Proc.P_starttime
		list = append(list, listedProcess{
			PID:    int(p.Proc.P_pid),
			Parent: int(p.Eproc.Ppid),
			// Same microseconds-since-epoch reading agents.Identify uses
			// (P_starttime), kept here so a gone process still has the
			// start the snapshot held for startedOutside's order check.
			Start: uint64(t.Sec)*1_000_000 + uint64(t.Usec),
		})
	}
	return list, nil
}

// processIDs is every process identifier in the kernel's process table.
func processIDs() ([]int, error) {
	list, err := processList()
	if err != nil {
		return nil, err
	}
	pids := make([]int, len(list))
	for i, p := range list {
		pids[i] = p.PID
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

// foreignProcess reports whether pid runs as another user's: its effective
// user, as the process table holds it (Eproc.Ucred.Uid), is not this
// process's effective user. It is read so that a listing passes over only
// the processes of another user's whose command line it could not read,
// rather than assuming that of every read that failed (processesWhere).
func foreignProcess(pid int) (bool, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return int(k.Eproc.Ucred.Uid) != os.Geteuid(), nil
}

// exitingProcess reports whether pid has begun to exit. This platform's
// process table reads an exited process as a zombie (agents.Identify), and
// a read of a running one that fails keeps a listing unsettled; a process
// still exiting is not told apart here, so it reports false.
func exitingProcess(int) (bool, error) { return false, nil }

// sstop is SSTOP from the platform's sys/proc.h, the process state of one
// a signal has stopped. It is spelled here because x/sys does not export it.
const sstop = 4

// stoppedProcess reports whether pid is stopped by a signal: its state in
// the process table (Proc.P_stat) is sstop. A process that is stopped runs
// none of its code, so it starts no process until it is continued.
func stoppedProcess(pid int) (bool, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return k.Proc.P_stat == sstop, nil
}
